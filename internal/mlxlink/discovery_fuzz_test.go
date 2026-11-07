package mlxlink

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
)

// validSysfsName reports whether name can be one path component.
func validSysfsName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 &&
		!strings.ContainsAny(name, "/\x00")
}

// FuzzSysfsDiscovery builds a one-device class/infiniband tree from fuzzed
// names. Discovery must not fail on a readable tree, must skip a virtual
// function, a device without numeric ports and a device whose name cannot be
// a label value, must pick the lowest numeric port, and every target it
// returns must be exportable: a sweep that fails on it counts errors under its
// labels, and a scrape afterwards succeeds.
func FuzzSysfsDiscovery(f *testing.F) {
	f.Add("mlx5_0", "1", "0000:1a:00.0", "ens1f0np0\n", false)
	f.Add("mlx5_0", "2,1,10", "0000:5e:00.0", "", false)
	f.Add("mlx5_4", "1", "0000:1a:00.1", "ens1f1v0\n", true)
	f.Add("ibp1s0f0", "1", "", "", false)
	f.Add("rocep26s0f0", "port,01,1", "0000:00:01.0", "  \n", false)
	// Neither may reach a label: Prometheus rejects label values that are not
	// valid UTF-8, and the poller panicked on the first error it counted.
	f.Add("mlx5_\xff", "1", "0000:1a:00.0", "", false)
	f.Add("mlx5_0", "1", "0000:1a:00.\xbf", "", false)

	f.Fuzz(func(t *testing.T, device, ports, pciAddr, netdev string, isVF bool) {
		if !validSysfsName(device) {
			return
		}
		root := t.TempDir()
		deviceDir := filepath.Join(root, classInfinibandPath, device)
		if err := os.MkdirAll(deviceDir, 0o755); err != nil {
			// The file system refuses the name (macOS rejects invalid
			// UTF-8, for one), so no sysfs could carry it either.
			return
		}

		lowest, numeric := 0, 0
		created := make(map[string]bool)
		for _, port := range strings.Split(ports, ",") {
			if !validSysfsName(port) || created[port] {
				continue
			}
			if err := os.MkdirAll(filepath.Join(deviceDir, portsDirName, port), 0o755); err != nil {
				return
			}
			created[port] = true
			if id, err := strconv.Atoi(port); err == nil {
				if numeric == 0 || id < lowest {
					lowest = id
				}
				numeric++
			}
		}

		// Like the kernel's, the device link ends in the PCI address. Without
		// one there is no link, and so no address and no VF either.
		var wantPCIAddr, wantNetDev string
		if validSysfsName(pciAddr) {
			link := "../../../devices/pci0000:00/" + pciAddr
			if err := os.Symlink(link, filepath.Join(deviceDir, deviceLinkName)); err != nil {
				return
			}
			if utf8.ValidString(pciAddr) {
				wantPCIAddr = pciAddr
			}
			if isVF {
				functionDir := filepath.Join(root, "devices", "pci0000:00", pciAddr)
				if err := os.MkdirAll(functionDir, 0o755); err != nil {
					return
				}
				if err := os.Symlink("../0000:00:00.0", filepath.Join(functionDir, physfnLinkName)); err != nil {
					return
				}
			}
		} else {
			isVF = false
		}
		// The netdev is read from the port directory the chosen number names.
		if portName := strconv.Itoa(lowest); numeric > 0 && created[portName] {
			ndevs := filepath.Join(deviceDir, portsDirName, portName, gidAttrsDirName, ndevsDirName)
			if err := os.MkdirAll(ndevs, 0o755); err != nil {
				return
			}
			if err := os.WriteFile(filepath.Join(ndevs, "0"), []byte(netdev), 0o644); err != nil {
				return
			}
			wantNetDev = strings.TrimSpace(netdev)
		}

		discovery := NewSysfsDiscovery(root, nil, newDiscardLogger())
		targets, err := discovery.Discover(context.Background())
		if err != nil {
			t.Fatalf("Discover failed on a readable tree: %v", err)
		}
		var want []Target
		if !isVF && numeric > 0 && utf8.ValidString(device) {
			want = []Target{{Device: device, Port: strconv.Itoa(lowest), PCIAddr: wantPCIAddr, NetDev: wantNetDev}}
		}
		if len(targets) != 0 || len(want) != 0 {
			if !reflect.DeepEqual(targets, want) {
				t.Fatalf("Discover\n got: %q\nwant: %q", targets, want)
			}
		}

		// A failing sweep labels its error counters with the target, and the
		// collectors label their self-monitoring with it.
		runErr := &RunError{Reason: ReasonTimeout, Err: errors.New("timed out")}
		runner := newFakeRunner(nil)
		runner.setResult(nil, runErr)
		runner.setPCIeEyeResult(nil, runErr)
		poller := NewPoller(discovery, runner, testPollInterval, newDiscardLogger(),
			withClock(newFakeClock(1)), WithShowPCIeEye(true))
		poller.sweep(context.Background())

		registry := prometheus.NewPedanticRegistry()
		registry.MustRegister(
			NewCollector(poller, collectorStaleAfter, newDiscardLogger()),
			NewPCIeEyeCollector(poller, collectorStaleAfter, newDiscardLogger()),
			poller.Errors(),
			poller.PCIeEyeErrors(),
		)
		if _, err := registry.Gather(); err != nil {
			t.Fatalf("a discovered device fails the scrape: %v", err)
		}
	})
}
