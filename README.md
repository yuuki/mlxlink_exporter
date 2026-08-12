# Prometheus mlxlink Exporter

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI](https://github.com/yuuki/mlxlink_exporter/actions/workflows/ci.yml/badge.svg)](https://github.com/yuuki/mlxlink_exporter/actions/workflows/ci.yml)
[![GitHub release](https://img.shields.io/github/v/release/yuuki/mlxlink_exporter)](https://github.com/yuuki/mlxlink_exporter/releases)

`mlxlink_exporter` which publishes physical link and optical module telemetry that NVIDIA's `mlxlink` reports: bit error ratios, per-lane raw errors, FEC error histograms, SerDes transmitter tuning, optional network and root-PCIe Eye measurements, transceiver diagnostics (temperature, voltage, bias current, optical power) and module inventory.

It is a separate binary because `mlxlink` is expensive: on the verified MFT 4.34.1 ConnectX-7 system, the baseline query took 0.77–0.78 s of wall time and most of that cost was fixed firmware-access overhead. The normal combined query, `mlxlink -d <device> -m -c --rx_fec_histogram --show_histogram --show_serdes_tx --json`, took 0.83 s. The same combined query with `--show_eye` also took 0.83 s in one measurement, while the separate root-PCIe Eye query took 0.33 s. These commands are far too slow to run inside a Prometheus scrape, so a background poller runs them on its own schedule and publishes decoded results into immutable in-memory snapshots; `/metrics` only reads those snapshots. Scrape frequency therefore has no effect on how often `mlxlink` runs, and a firmware hang or an extra privilege never blocks a Prometheus scrape.

The default listen address is `:9880`.

### Requirements
- Linux only.
- [NVIDIA MFT](https://network.nvidia.com/products/adapter-software/firmware-tools/) installed, providing the `mlxlink` binary (`--mlxlink-path`, default `/usr/bin/mlxlink`). The exporter exits with status 1 at start-up if that path does not exist.
- Devices are addressed by their IB device name (`mlxlink -d mlx5_0`), so `mst start` and `/dev/mst/*` device nodes are **not** required.
- Read access to `/sys/class/infiniband` for device discovery.
- Verified against MFT 4.34.1 output from one ConnectX-7 system. Decoder tests use captured baseline, FEC/SerDes, network Eye and root-PCIe Eye responses under `internal/mlxlink/testdata/mlxlink/`. The serial number in the Eye capture is redacted. Other MFT releases and adapter families are not yet qualified.

### Install
Release archives carry the binary, this README, the LICENSE and the systemd assets for `linux/amd64` and `linux/arm64`. Verify the download against `mlxlink_exporter_checksums.txt` from the same release.

```bash
VERSION=0.1.0
curl -fsSLO https://github.com/yuuki/mlxlink_exporter/releases/download/v${VERSION}/mlxlink_exporter_${VERSION}_linux_amd64.tar.gz
tar xzf mlxlink_exporter_${VERSION}_linux_amd64.tar.gz
sudo install -Dm0755 mlxlink_exporter /usr/local/bin/mlxlink_exporter
```

See [docs/deployment.md](docs/deployment.md) for the full systemd procedure, including the privilege escalation order.

A multi-platform image is published to `ghcr.io/yuuki/mlxlink_exporter`:

```bash
docker pull ghcr.io/yuuki/mlxlink_exporter:v0.1.0
```

The image contains the exporter only. It cannot collect anything on its own, because `mlxlink` belongs to MFT on the host: the container needs `/sys/class/infiniband` and the MFT installation mounted in, plus whatever firmware-access privilege the host requires. Container deployment has not been qualified on real hardware; the systemd unit in `deploy/systemd/` is the supported path, and the image is offered as a distribution convenience for operators who already run their exporters this way.

### Build and run
```bash
make build   # compiles ./mlxlink_exporter
```

```bash
./mlxlink_exporter \
  --listen-address=":9880" \
  --mlxlink-path="/usr/bin/mlxlink" \
  --poll-interval=30s
```

Both Eye flags are optional and default to `false`. After qualifying the corresponding command as the service user, add `--show-eye`, `--show-pcie-eye`, or both to opt in. Leave `--show-eye` off where the query is not supported: its fallback is retried every sweep, so each device then costs two `mlxlink` invocations per sweep (three if the combined extras are rejected too) while `mlxlink_collection_errors_total{reason="exit_error"}` keeps rising.

Recommended settings: keep `--poll-interval=30s` and scrape every 15–30 s. Scraping more often is free, because a scrape never executes `mlxlink`; only `--poll-interval` controls how often the tool runs. A sweep visits every device sequentially, so with N devices one normal network sweep costs about N × 0.83 s on the verified hardware. When `--show-pcie-eye` is enabled, every network device is collected first, then the root-PCIe Eye query runs once per device at low priority while global `mlxlink` concurrency remains one. A fallback or PCIe Eye query extends the sweep, so verify that the complete sweep stays below the poll interval.

To print build information without starting the server, add `--version`.

### Configuration
Every flag except `--version` has an equivalent environment variable, twelve in total. Environment values provide defaults; explicit CLI flags take precedence.

| Flag | Environment | Default | Description |
| ---- | ----------- | ------- | ----------- |
| `--listen-address` | `MLXLINK_EXPORTER_LISTEN_ADDRESS` | `:9880` | HTTP listen address |
| `--metrics-path` | `MLXLINK_EXPORTER_METRICS_PATH` | `/metrics` | Metrics endpoint path |
| `--health-path` | `MLXLINK_EXPORTER_HEALTH_PATH` | `/healthz` | Liveness endpoint path, always `200 OK` |
| `--ready-path` | `MLXLINK_EXPORTER_READY_PATH` | `/readyz` | Readiness endpoint path, `503` until one device has been collected |
| `--log-level` | `MLXLINK_EXPORTER_LOG_LEVEL` | `info` | Log verbosity (`debug`, `info`, `warn`, `error`) |
| `--mlxlink-path` | `MLXLINK_EXPORTER_MLXLINK_PATH` | `/usr/bin/mlxlink` | Path to the `mlxlink` binary |
| `--sysfs-root` | `MLXLINK_EXPORTER_SYSFS_ROOT` | `/sys` | Root directory used to discover RDMA devices |
| `--poll-interval` | `MLXLINK_EXPORTER_POLL_INTERVAL` | `30s` | Interval between background sweeps over all devices |
| `--command-timeout` | `MLXLINK_EXPORTER_COMMAND_TIMEOUT` | `3s` | Maximum duration of a single `mlxlink` invocation |
| `--exclude-devices` | `MLXLINK_EXPORTER_EXCLUDE_DEVICES` | `` | Comma-separated list of RDMA devices to skip (e.g., `mlx5_0,mlx5_1`) |
| `--show-eye` | `MLXLINK_EXPORTER_SHOW_EYE` | `false` | Add network-port Eye telemetry to the combined query |
| `--show-pcie-eye` | `MLXLINK_EXPORTER_SHOW_PCIE_EYE` | `false` | Collect root-PCIe Eye telemetry with a separate low-priority query |
| `--version` | – | `false` | Print build information and exit |

### Metrics
Network-port families carry the labels `device`, `port` and `pci_addr`; per-lane families add `lane`. Root-PCIe Eye families carry `device` and `pci_addr`, but deliberately have no `port` label because they describe the PCIe link rather than a network port. Non-Eye lane numbers use the zero-based position in the reported list. Eye lane numbers come from the explicit `Lane` list.

Link and inventory:
- `mlxlink_link_info{device,port,pci_addr,state,physical_state,speed,width,fec,auto_negotiation}` – Gauge set to `1` with the port's operational attributes as labels. Not published when every attribute is empty.
- `mlxlink_module_info{device,port,pci_addr,identifier,vendor,part_number,serial_number,revision,firmware_version,active_host_compliance,active_media_compliance,cable_type}` – Gauge set to `1` with the transceiver inventory as labels. `firmware_version` is the module firmware, not the adapter firmware. Not published when every attribute is empty.

Physical layer counters:
- `mlxlink_effective_physical_errors_total{device,port,pci_addr}` – Effective physical errors.
- `mlxlink_raw_physical_errors_total{device,port,pci_addr,lane}` – Raw physical errors per lane.
- `mlxlink_link_down_total{device,port,pci_addr}` – Link down events.
- `mlxlink_link_error_recovery_total{device,port,pci_addr}` – Link error recovery events.

FEC histogram counters:
- `mlxlink_rx_fec_codewords_total{device,port,pci_addr,bin,error_count_min,error_count_max}` – Counter of received FEC codewords in each corrected-error range reported by `mlxlink`. A vendor range `[N]` is exported with equal `error_count_min` and `error_count_max`; `[low:high]` preserves both inclusive bounds. The vendor bins are disjoint, not cumulative Prometheus histogram buckets, so queries must sum the desired ranges explicitly. The histogram can be cleared with `mlxlink --clear_histogram`, which this exporter never invokes, and may also reset with the adapter, hardware or firmware.

SerDes transmitter tuning (gauges with vendor-defined tuning codes; the verified output provides no physical units):
- `mlxlink_serdes_tx_fir_coefficient{device,port,pci_addr,lane,tap}` – Transmitter FIR coefficient. `tap` is allowlisted to `pre3`, `pre2`, `pre1`, `main` or `post1`; unknown vendor parameters are not exported.
- `mlxlink_serdes_tx_drive_amplitude{device,port,pci_addr,lane}` – Transmitter drive-amplitude tuning code.

Optional Eye telemetry (gauges with vendor-defined scores and no physical units):
- `mlxlink_eye_fom{device,port,pci_addr,lane,stage}` – Network-port figure of merit. `stage` is `initial` or `last`.
- `mlxlink_eye_grade{device,port,pci_addr,lane,position}` – Network-port grade. `position` is `upper`, `mid` or `lower`.
- `mlxlink_pcie_eye_fom{device,pci_addr,lane,stage}` – Root-PCIe figure of merit. `stage` is `initial` or `last`.

`FOM Mode` is intentionally not exported as a metric or label: its value set has not been established across MFT and hardware versions.

Bit error ratios (gauges, dimensionless):
- `mlxlink_effective_physical_ber{device,port,pci_addr}` – Effective physical BER.
- `mlxlink_raw_physical_ber{device,port,pci_addr}` – Raw physical BER.
- `mlxlink_raw_physical_ber_lane{device,port,pci_addr,lane}` – Raw physical BER per lane.

Digital diagnostic monitoring (gauges). Only the two units `mlxlink` reports with a milli prefix are converted; temperature and optical power are exported exactly as reported:
- `mlxlink_module_temperature_celsius{device,port,pci_addr}` – Module temperature in degrees Celsius.
- `mlxlink_module_voltage_volts{device,port,pci_addr}` – Module supply voltage in volts, converted from the millivolts `mlxlink` reports.
- `mlxlink_module_bias_current_amperes{device,port,pci_addr,lane}` – Laser bias current in amperes, converted from milliamperes.
- `mlxlink_module_rx_power_dbm{device,port,pci_addr,lane}` – Received optical power in dBm.
- `mlxlink_module_tx_power_dbm{device,port,pci_addr,lane}` – Transmitted optical power in dBm.

Fault and state flags (gauges, `0` or `1`):
- `mlxlink_module_fw_fault{device,port,pci_addr}` – Module firmware fault.
- `mlxlink_datapath_fw_fault{device,port,pci_addr}` – Datapath firmware fault.
- `mlxlink_tx_fault{device,port,pci_addr,lane}` – Transmitter fault.
- `mlxlink_tx_los{device,port,pci_addr,lane}` – Transmitter loss of signal.
- `mlxlink_rx_los{device,port,pci_addr,lane}` – Receiver loss of signal.
- `mlxlink_tx_cdr_loss_of_lock{device,port,pci_addr,lane}` – Transmitter CDR loss of lock.
- `mlxlink_rx_cdr_loss_of_lock{device,port,pci_addr,lane}` – Receiver CDR loss of lock.
- `mlxlink_datapath_active{device,port,pci_addr,lane}` – `1` only when the lane reports `DPActivated`.

Exporter self-monitoring:
- `mlxlink_collector_up{device,port,pci_addr}` – `1` when the most recent poll of that device succeeded, `0` otherwise.
- `mlxlink_collection_duration_seconds{device,port,pci_addr}` – Duration of the latest collection attempt, including every invocation in the fallback chain.
- `mlxlink_collection_last_success_timestamp_seconds{device,port,pci_addr}` – Unix timestamp of the last successful collection. Not published for a device that has never succeeded, so the series never reports 1970.
- `mlxlink_collection_errors_total{device,port,pci_addr,reason}` – Collection failure events, including a combined-query error that a fallback recovered from. `reason` is one of `timeout`, `command_not_found`, `permission_denied`, `exit_error`, `invalid_json`, `output_too_large`, `unknown`.
- `mlxlink_sweep_overlaps_total` – Ticks dropped because the previous sweep was still running. Process-wide and unlabeled; a growing value means the poll interval is shorter than a full sweep. This counter replaces the former `reason="overlapping"` label on the two error counters, which no longer exists.

Root-PCIe Eye self-monitoring is registered only when `--show-pcie-eye` is enabled and has no `port` label:
- `mlxlink_pcie_eye_collector_up{device,pci_addr}` – `1` when the most recent root-PCIe Eye poll succeeded, `0` otherwise.
- `mlxlink_pcie_eye_collection_duration_seconds{device,pci_addr}` – Duration of the latest root-PCIe Eye attempt.
- `mlxlink_pcie_eye_collection_last_success_timestamp_seconds{device,pci_addr}` – Unix timestamp of the last successful root-PCIe Eye collection; omitted until the first success.
- `mlxlink_pcie_eye_collection_errors_total{device,pci_addr,reason}` – Root-PCIe Eye failures, using the same closed `reason` set as network collection.

A value that `mlxlink` reports as `N/A` produces no sample at all rather than a zero. For the measurement families (BER, temperature, voltage, bias current, optical power, raw errors) only the affected lane is dropped. The flag families above are all-or-nothing: if any lane of `mlxlink_tx_fault` and friends is unreadable, or reports anything other than `0`/`1`, the whole family is omitted for that port rather than published with renumbered lanes.

### Operational notes
- **Physical counters can be cleared.** The physical counters live in the adapter firmware and can be reset by other tooling with `mlxlink --clear_counters`, as well as by firmware resets or link training. `mlxlink` reports how long ago that happened as `Time Since Last Clear [Min]`, which this exporter does not export.
- **The FEC histogram has separate reset semantics.** `mlxlink --clear_histogram` clears the histogram occurrences independently of `--clear_counters`; adapter, hardware or firmware resets may also return them to zero. The exporter invokes neither explicit reset operation, and operators should not run them merely to monitor the link because doing so destroys counter history. `rate()` and `increase()` detect a reset, but a reset inside an evaluation window still hides the errors that preceded it, so treat a sudden return to zero as "the histogram was reset", not "errors stopped".
- **Only non-zero exits trigger fallback.** Without network Eye, a rejected combined query falls back directly to `mlxlink -d <device> -m -c --json`. With `--show-eye`, the Eye-enabled combined query first falls back to the normal combined query; if that also exits non-zero, it falls back to baseline. A successful normal-combined fallback omits only Eye data and retains FEC/SerDes data. A successful baseline fallback omits Eye, FEC and SerDes data. Each rejected query increments `mlxlink_collection_errors_total{reason="exit_error"}`, while the successful fallback leaves `mlxlink_collector_up=1`; duration covers the whole staged attempt. Timeouts, permission failures, oversized output, invalid JSON and shutdown cancellation do not advance to the next query. Nothing is remembered between sweeps: the staged attempt runs again on every sweep, so a query that always fails is paid for every time.
- **PCIe Eye failures are isolated.** Root-PCIe Eye collection runs after all network collection, has no fallback, and never changes network snapshots, readiness or `mlxlink_collector_up`. A failure sets its own `up` metric to `0` and retains the previous PCIe Eye values until they become stale.
- **Stale data is suppressed.** If a device has not been collected successfully for longer than `--poll-interval` × 5 (150 s by default), its measurement series stop being exported while the self-monitoring series continue. This is what distinguishes "the link is fine" from "we stopped being able to ask".
- **Overlap accounting is approximate.** If a sweep takes longer than `--poll-interval`, the tick that could not start a sweep is dropped and counted in `mlxlink_sweep_overlaps_total`. Go tickers coalesce missed ticks, so a sweep that overruns several intervals is still counted once: use the metric to detect that the interval is too short, not to count exactly how many sweeps were lost.
- **The container image carries no MFT.** `mlxlink` belongs to MFT and talks to the adapter firmware, so it is deliberately absent from `ghcr.io/yuuki/mlxlink_exporter`: the image holds the exporter alone and collects nothing until `/sys/class/infiniband`, the MFT installation and the firmware-access privilege the host requires are all provided to the container. That arrangement is unqualified on real hardware; running on the host under the systemd unit in `deploy/systemd/` is the supported path.
- **Multi-port adapters.** `mlxlink` is invoked once per device without `-p`, so only the lowest port number of a device is collected.
- **A host with no RDMA devices** stays at `503` on `/readyz` forever, by design: there is nothing to collect. `/healthz` remains `200`.
- **Eye telemetry is opt-in and narrowly qualified.** Both Eye flags default to `false`. Network combined Eye took 0.83 s and the separate PCIe Eye query took 0.33 s in single measurements on MFT 4.34.1/ConnectX-7. These are not multi-run latency guarantees, and other MFT releases, adapter families, cables, line rates and link states remain unverified.
- Running unprivileged, and how to grant only the privileges your host actually needs, is covered in [docs/deployment.md](docs/deployment.md).

### Joining with rdma_exporter metrics
The two exporters are separate scrape targets (`:9879` and `:9880`), so their `instance` labels never match and the default vector matching cannot be used. Join on the identity labels both sides carry, `device`, `port` and `pci_addr`, with `on(...)` so that `instance` and `job` are ignored:

```promql
# Raw BER annotated with link layer and PF/VF identity from rdma_port_info.
mlxlink_raw_physical_ber
  * on(device, port, pci_addr) group_left(link_layer, link_speed, is_vf, pf_device)
  rdma_port_info
```

Across more than one host this form is wrong: the same `device`/`port`/`pci_addr` exists on every machine, so it would match series from other hosts. Either add a host label to both jobs with `relabel_configs`, or derive one in the query:

```promql
label_replace(mlxlink_module_temperature_celsius, "host", "$1", "instance", "([^:]+):.*")
  * on(host, device, port, pci_addr) group_left(link_layer, is_vf, pf_device)
label_replace(rdma_port_info, "host", "$1", "instance", "([^:]+):.*")
```

Be careful about which labels `group_left` carries over. `rdma_port_info` exposes `link_layer`, `state`, `phys_state`, `link_width`, `link_speed`, `is_vf` and `pf_device`; of these, `state` also exists on `mlxlink_link_info`. Carrying it does not raise an error — the value from `rdma_port_info` silently replaces the mlxlink one, so the result claims a sysfs port state under a metric named for the mlxlink state. Leave `state` out, or rename it first:

```promql
mlxlink_link_info
  * on(device, port, pci_addr) group_left(sysfs_state)
  label_replace(rdma_port_info, "sysfs_state", "$1", "state", "(.*)")
```

The value families (`mlxlink_raw_physical_ber`, `mlxlink_module_*`, …) only carry `device`, `port`, `pci_addr` and `lane`, so any of the labels above are safe there.

The `instance` regex above assumes a `host:port` target. Targets written as bracketed IPv6 (`[2001:db8::1]:9880`) need a different pattern, for example `(\[.*\]|[^:]+):.*`.

## Testing
```bash
go test ./...
```

For deterministic builds in shared environments, you can pin Go's caches locally:

```bash
GOCACHE=$(pwd)/.gocache GOMODCACHE=$(pwd)/.gomodcache go test ./...
```

`internal/mlxlink/testdata/sysfs` contains fixture trees used in unit tests to emulate sysfs layouts.

## Deployment

- A systemd unit file and an opt-in root override are available under `deploy/systemd/`; see [docs/deployment.md](docs/deployment.md).
- The published container image ships the exporter without MFT, so it is a distribution convenience rather than a qualified deployment; see the operational note above.

## Development Notes

- Architectural decisions and future work are documented in [docs/design.md](docs/design.md).
- Logging uses the Go standard library `log/slog`. Set `--log-level=debug` for detailed collection traces.

## License
This project is licensed under the MIT License. See `LICENSE` for full text.

