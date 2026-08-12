package mlxlink

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// clock and ticker isolate the poller from wall clock time so tests can drive
// sweeps deterministically.
type clock interface {
	Now() time.Time
	NewTicker(d time.Duration) ticker
}

type ticker interface {
	C() <-chan time.Time
	Stop()
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) NewTicker(d time.Duration) ticker { return &realTicker{t: time.NewTicker(d)} }

type realTicker struct{ t *time.Ticker }

func (r *realTicker) C() <-chan time.Time { return r.t.C }

func (r *realTicker) Stop() { r.t.Stop() }

// discoverer and commandRunner are the consumer side views of *SysfsDiscovery
// and *ExecRunner: the poller needs nothing beyond these calls, and tests
// substitute fakes for them.
type discoverer interface {
	Discover(ctx context.Context) ([]Target, error)
}

type commandRunner interface {
	Run(ctx context.Context, device string) ([]byte, error)
	RunWithEye(ctx context.Context, device string) ([]byte, error)
	RunPCIeEye(ctx context.Context, device string) ([]byte, error)
	RunBaseline(ctx context.Context, device string) ([]byte, error)
}

// DeviceSnapshot is the last known state of one device. Data and LastSuccess
// survive failed sweeps so a transient error does not blank out the metrics; a
// zero LastSuccess means the device has never been collected successfully.
type DeviceSnapshot struct {
	Target       Target
	Data         PortData
	LastSuccess  time.Time
	LastError    ErrorReason
	LastDuration time.Duration
}

// PCIeEyeSnapshot is the independently collected Eye state of one device's
// root PCIe link. A PCIe failure never changes the network DeviceSnapshot.
type PCIeEyeSnapshot struct {
	Target       Target
	Data         PCIeEye
	LastSuccess  time.Time
	LastError    ErrorReason
	LastDuration time.Duration
}

// snapshotSet is an immutable set of snapshots keyed by device name. It is
// built once per publish and replaced wholesale, never mutated after being
// stored, so scrapes read it without a lock.
type snapshotSet[T any] struct {
	byDevice map[string]T
}

// lookup finds a device by name; a nil set simply has no devices, which is the
// state before the first sweep publishes.
func (s *snapshotSet[T]) lookup(device string) (T, bool) {
	if s == nil {
		var zero T
		return zero, false
	}
	snapshot, ok := s.byDevice[device]
	return snapshot, ok
}

// Poller collects mlxlink data in the background so that scrapes never execute
// mlxlink themselves. A single sweep loop walks every device in turn, which
// keeps concurrency at one and makes overlapping runs structurally impossible.
type Poller struct {
	discovery   discoverer
	runner      commandRunner
	interval    time.Duration
	showEye     bool
	showPCIeEye bool
	clk         clock
	logger      *slog.Logger

	store         atomic.Pointer[snapshotSet[DeviceSnapshot]]
	pcieEyeStore  atomic.Pointer[snapshotSet[PCIeEyeSnapshot]]
	errors        *prometheus.CounterVec
	pcieEyeErrors *prometheus.CounterVec
	overlaps      prometheus.Counter
	ready         atomic.Bool
}

// PollerOption customises a Poller at construction time.
type PollerOption func(*Poller)

// withClock replaces the time source; tests use it to drive sweeps.
func withClock(c clock) PollerOption {
	return func(p *Poller) { p.clk = c }
}

// WithShowEye enables the network-port Eye section in the normal query.
func WithShowEye(enabled bool) PollerOption {
	return func(p *Poller) { p.showEye = enabled }
}

// WithShowPCIeEye enables the low-priority root PCIe Eye query.
func WithShowPCIeEye(enabled bool) PollerOption {
	return func(p *Poller) { p.showPCIeEye = enabled }
}

// NewPoller returns a poller that collects from discovery through runner every
// interval. A nil logger falls back to slog.Default.
func NewPoller(discovery discoverer, runner commandRunner, interval time.Duration, logger *slog.Logger, opts ...PollerOption) *Poller {
	if logger == nil {
		logger = slog.Default()
	}

	p := &Poller{
		discovery: discovery,
		runner:    runner,
		interval:  interval,
		clk:       realClock{},
		logger:    logger,
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mlxlink_collection_errors_total",
			Help: "Total number of mlxlink query and decode errors by reason.",
		}, []string{"device", "port", "pci_addr", "reason"}),
		pcieEyeErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mlxlink_pcie_eye_collection_errors_total",
			Help: "Total number of PCIe Eye query and decode errors by reason.",
		}, []string{"device", "pci_addr", "reason"}),
		overlaps: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mlxlink_sweep_overlaps_total",
			Help: "Total number of ticks dropped because the previous sweep was still running. A growing value means the poll interval is shorter than a full sweep.",
		}),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Errors exposes the collection error counter for registration by the caller.
func (p *Poller) Errors() prometheus.Collector { return p.errors }

// PCIeEyeErrors exposes the independent PCIe Eye collection error counter.
func (p *Poller) PCIeEyeErrors() prometheus.Collector { return p.pcieEyeErrors }

// Overlaps exposes the sweep overlap counter for registration by the caller.
func (p *Poller) Overlaps() prometheus.Collector { return p.overlaps }

// Snapshots returns the current immutable snapshot set, or nil before the first
// device has been collected.
func (p *Poller) Snapshots() *snapshotSet[DeviceSnapshot] { return p.store.Load() }

// PCIeEyeSnapshots returns the independent root PCIe Eye snapshots.
func (p *Poller) PCIeEyeSnapshots() *snapshotSet[PCIeEyeSnapshot] { return p.pcieEyeStore.Load() }

// Ready reports whether at least one device has ever been collected
// successfully. It never returns to false: once data exists, a later failure
// leaves the exporter serving that data rather than dropping out of service.
func (p *Poller) Ready() bool { return p.ready.Load() }

// Run sweeps every device immediately and then once per interval, blocking
// until ctx is done. Call it once.
func (p *Poller) Run(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	// Sweep before waiting for the first tick so readiness does not lag a full
	// interval behind startup.
	p.sweep(ctx)

	t := p.clk.NewTicker(p.interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
			p.sweep(ctx)
			p.drainTicks(ctx, t)
		}
	}
}

// drainTicks discards the single tick that may have fired while a sweep was
// running and counts it: it is the sweep that could not start. Dropping it
// rather than sweeping again immediately is the point, so a slow sweep does not
// chain into back-to-back mlxlink invocations against the firmware.
//
// Exactly one tick is taken per sweep. A real ticker coalesces the ticks it
// missed, so no more than one can ever be pending however long a sweep runs,
// and taking one keeps the drain finite and free of overcounting even against a
// fake that queues more. The channel length cannot be used to size this:
// since Go 1.23 a ticker channel reports len 0 while a tick is pending.
//
// A tick that arrives just as the drain runs can be consumed here instead of
// starting a sweep, which slips that sweep by one interval and counts one
// overlap that did not happen. Together with coalescing, overlaps are
// undercounted; the signal is meant for spotting a poll interval that is too
// short, not exact accounting.
func (p *Poller) drainTicks(ctx context.Context, t ticker) {
	select {
	case <-t.C():
		if ctx.Err() != nil {
			return
		}
		p.overlaps.Inc()
		// Logged after counting so the record marks a completed accounting.
		p.logger.Warn("mlxlink sweep did not finish before the next tick",
			"poll_interval", p.interval.String())
	default:
	}
}

// sweep collects every discovered device once, in order. Discovery is repeated
// on each sweep so hotplugged devices appear and removed ones fall out of the
// published set.
func (p *Poller) sweep(ctx context.Context) {
	targets, err := p.discovery.Discover(ctx)
	if err != nil {
		if ctx.Err() == nil {
			p.logger.Warn("mlxlink device discovery failed", "err", err)
		}
		// Keep serving the previous set rather than dropping every device
		// because sysfs was momentarily unreadable.
		return
	}

	completed := sweepDevices(ctx, &p.store, targets, p.collect, func(snapshot DeviceSnapshot) {
		// Readiness is announced only once the data behind it is published:
		// a reader that sees Ready() must be able to scrape that device.
		if snapshot.LastError == "" {
			p.ready.Store(true)
		}
	})
	if !completed {
		// Shutting down: leave the published sets as they are.
		return
	}

	if p.showPCIeEye {
		sweepDevices(ctx, &p.pcieEyeStore, targets, p.collectPCIeEye, nil)
	}
}

// sweepDevices collects targets in order and publishes after each one, so a
// scrape landing mid-sweep sees this sweep's data for the devices already
// visited and the previous data for the ones still pending, never a gap. The
// working map is seeded from the previous set restricted to the targets
// discovered now, which is what makes a device that disappeared fall out at the
// first publish of the sweep. onPublish, when set, runs once the device is
// visible to scrapes.
//
// It reports whether the sweep ran to completion: a false from collect means
// shutting down, where the published set must be left as it is.
func sweepDevices[T any](
	ctx context.Context,
	store *atomic.Pointer[snapshotSet[T]],
	targets []Target,
	collect func(context.Context, Target, *snapshotSet[T]) (T, bool),
	onPublish func(T),
) bool {
	if len(targets) == 0 {
		// The loop below has no device left to publish with, and what was
		// published before belongs to devices that are all gone.
		store.Store(&snapshotSet[T]{})
		return true
	}

	previous := store.Load()
	working := make(map[string]T, len(targets))
	for _, target := range targets {
		if snapshot, ok := previous.lookup(target.Device); ok {
			working[target.Device] = snapshot
		}
	}

	for _, target := range targets {
		snapshot, ok := collect(ctx, target, previous)
		if !ok {
			return false
		}
		working[target.Device] = snapshot
		// The published map is a copy: the working map keeps being written to
		// for the rest of the sweep, and a stored set is immutable.
		store.Store(&snapshotSet[T]{byDevice: maps.Clone(working)})

		if onPublish != nil {
			onPublish(snapshot)
		}
	}
	return true
}

func (p *Poller) collectPCIeEye(
	ctx context.Context,
	target Target,
	previous *snapshotSet[PCIeEyeSnapshot],
) (PCIeEyeSnapshot, bool) {
	snapshot, _ := previous.lookup(target.Device)
	snapshot.Target = target

	start := p.clk.Now()
	raw, err := p.runner.RunPCIeEye(ctx, target.Device)
	now := p.clk.Now()
	snapshot.LastDuration = now.Sub(start)
	if ctx.Err() != nil {
		return PCIeEyeSnapshot{}, false
	}
	if err != nil {
		p.recordPCIeEyeFailure(&snapshot, ReasonFromError(err), err)
		return snapshot, true
	}

	data, err := DecodePCIeEye(raw)
	if ctx.Err() != nil {
		return PCIeEyeSnapshot{}, false
	}
	if err != nil {
		p.recordPCIeEyeFailure(&snapshot, ReasonInvalidJSON, err)
		return snapshot, true
	}
	snapshot.Data = data
	snapshot.LastSuccess = now
	snapshot.LastError = ""
	p.logger.Debug("mlxlink PCIe Eye collected",
		"device", target.Device, "pci_addr", target.PCIAddr, "duration", snapshot.LastDuration)
	return snapshot, true
}

// collect runs mlxlink for one target and folds the result into the previous
// snapshot. The boolean is false only while shutting down, where the failure is
// expected and must not be recorded as a collection error.
func (p *Poller) collect(ctx context.Context, target Target, previous *snapshotSet[DeviceSnapshot]) (DeviceSnapshot, bool) {
	snapshot, _ := previous.lookup(target.Device)
	// Discovery may have refreshed the labels of an existing device.
	snapshot.Target = target

	start := p.clk.Now()
	if p.showEye {
		return p.collectWithEye(ctx, target, snapshot, start)
	}
	return p.collectCombined(ctx, target, snapshot, start)
}

func (p *Poller) collectWithEye(
	ctx context.Context,
	target Target,
	snapshot DeviceSnapshot,
	start time.Time,
) (DeviceSnapshot, bool) {
	raw, err := p.runner.RunWithEye(ctx, target.Device)
	now := p.clk.Now()
	snapshot.LastDuration = now.Sub(start)
	if ctx.Err() != nil {
		return DeviceSnapshot{}, false
	}

	if err != nil {
		if ReasonFromError(err) == ReasonExitError {
			fallback, ok := p.collectCombined(ctx, target, snapshot, start)
			if !ok {
				return DeviceSnapshot{}, false
			}
			if ctx.Err() != nil {
				return DeviceSnapshot{}, false
			}
			p.countError(target, ReasonExitError)
			p.logger.Debug("mlxlink Eye query required combined fallback",
				"device", target.Device, "port", target.Port, "pci_addr", target.PCIAddr,
				"reason", ReasonExitError.String(), "duration", fallback.LastDuration)
			return fallback, true
		}
		p.recordFailure(&snapshot, ReasonFromError(err), err)
		return snapshot, true
	}

	data, err := Decode(raw)
	if ctx.Err() != nil {
		return DeviceSnapshot{}, false
	}
	if err != nil {
		p.recordFailure(&snapshot, ReasonInvalidJSON, err)
		return snapshot, true
	}
	return p.recordSuccess(snapshot, target, data, now), true
}

func (p *Poller) collectCombined(
	ctx context.Context,
	target Target,
	snapshot DeviceSnapshot,
	start time.Time,
) (DeviceSnapshot, bool) {
	raw, err := p.runner.Run(ctx, target.Device)
	now := p.clk.Now()
	snapshot.LastDuration = now.Sub(start)
	if ctx.Err() != nil {
		return DeviceSnapshot{}, false
	}

	if err != nil {
		if ReasonFromError(err) == ReasonExitError {
			return p.collectBaseline(ctx, target, snapshot, start)
		}
		p.recordFailure(&snapshot, ReasonFromError(err), err)
		return snapshot, true
	}

	data, err := Decode(raw)
	if ctx.Err() != nil {
		return DeviceSnapshot{}, false
	}
	if err != nil {
		// Decode returns a plain error by design, so the reason is assigned
		// here; ReasonFromError would flatten it to unknown.
		p.recordFailure(&snapshot, ReasonInvalidJSON, err)
		return snapshot, true
	}
	// Only RunWithEye makes Eye data authoritative. A normal combined query may
	// still include an unsolicited section, but the opt-in contract omits it.
	data.Eye = Eye{}
	return p.recordSuccess(snapshot, target, data, now), true
}

func (p *Poller) recordSuccess(snapshot DeviceSnapshot, target Target, data PortData, now time.Time) DeviceSnapshot {
	snapshot.Data = data
	snapshot.LastSuccess = now
	// Readiness is deliberately not announced here: the caller publishes the
	// snapshot first. An empty LastError is what marks this round a success.
	snapshot.LastError = ""

	p.logger.Debug("mlxlink device collected",
		"device", target.Device, "port", target.Port, "pci_addr", target.PCIAddr,
		"duration", snapshot.LastDuration)
	return snapshot
}

// collectBaseline retries the original module and counter query after a
// combined query exits unsuccessfully. Only exit errors reach this method:
// failures such as timeouts may affect the baseline query too and must not
// extend the sweep with another invocation.
func (p *Poller) collectBaseline(
	ctx context.Context,
	target Target,
	snapshot DeviceSnapshot,
	start time.Time,
) (DeviceSnapshot, bool) {
	if ctx.Err() != nil {
		return DeviceSnapshot{}, false
	}

	raw, fallbackErr := p.runner.RunBaseline(ctx, target.Device)
	now := p.clk.Now()
	snapshot.LastDuration = now.Sub(start)
	if ctx.Err() != nil {
		return DeviceSnapshot{}, false
	}

	if fallbackErr != nil {
		p.recordCombinedFallback(target, snapshot.LastDuration)
		p.recordFailure(&snapshot, ReasonFromError(fallbackErr), fallbackErr)
		return snapshot, true
	}

	data, err := Decode(raw)
	if ctx.Err() != nil {
		return DeviceSnapshot{}, false
	}
	p.recordCombinedFallback(target, snapshot.LastDuration)
	if err != nil {
		p.recordFailure(&snapshot, ReasonInvalidJSON, err)
		return snapshot, true
	}

	// A fallback response is authoritative only for the baseline families.
	// Some mlxlink versions may still include optional sections, but publishing
	// them would hide that the combined query which requested them failed.
	data.FECHistogram = nil
	data.SerDesTX = SerDesTX{}
	data.Eye = Eye{}
	snapshot.Data = data
	snapshot.LastSuccess = now
	snapshot.LastError = ""
	p.logger.Debug("mlxlink device collected with baseline fallback",
		"device", target.Device, "port", target.Port, "pci_addr", target.PCIAddr,
		"duration", snapshot.LastDuration)
	return snapshot, true
}

// recordCombinedFallback accounts for the rejected combined query only after
// the fallback result is known, so shutdown does not become a collection error.
func (p *Poller) recordCombinedFallback(target Target, duration time.Duration) {
	p.countError(target, ReasonExitError)
	p.logger.Debug("mlxlink combined query required baseline fallback",
		"device", target.Device, "port", target.Port, "pci_addr", target.PCIAddr,
		"reason", ReasonExitError.String(), "duration", duration)
}

// recordFailure keeps the previous data and last success timestamp: stale data
// with a visible error beats no data at all, and staleness is bounded by the
// collector.
func (p *Poller) recordFailure(snapshot *DeviceSnapshot, reason ErrorReason, err error) {
	snapshot.LastError = reason
	p.countError(snapshot.Target, reason)

	p.logger.Warn("mlxlink collection failed",
		"device", snapshot.Target.Device, "port", snapshot.Target.Port,
		"pci_addr", snapshot.Target.PCIAddr, "reason", reason.String(),
		"duration", snapshot.LastDuration, "err", logCause(err))
}

// logCause strips a *RunError down to the error underneath it. A *RunError
// renders its captured stderr, which would put up to 4 KiB of tool output into
// the warning of every failed sweep; the runner already records it at debug
// level, where the volume is asked for.
func logCause(err error) error {
	var runErr *RunError
	if errors.As(err, &runErr) {
		return runErr.Err
	}
	return err
}

func (p *Poller) countError(target Target, reason ErrorReason) {
	p.errors.WithLabelValues(target.Device, target.Port, target.PCIAddr, reason.String()).Inc()
}

func (p *Poller) recordPCIeEyeFailure(snapshot *PCIeEyeSnapshot, reason ErrorReason, err error) {
	snapshot.LastError = reason
	p.countPCIeEyeError(snapshot.Target, reason)

	p.logger.Warn("mlxlink PCIe Eye collection failed",
		"device", snapshot.Target.Device, "pci_addr", snapshot.Target.PCIAddr,
		"reason", reason.String(), "duration", snapshot.LastDuration, "err", logCause(err))
}

func (p *Poller) countPCIeEyeError(target Target, reason ErrorReason) {
	p.pcieEyeErrors.WithLabelValues(target.Device, target.PCIAddr, reason.String()).Inc()
}
