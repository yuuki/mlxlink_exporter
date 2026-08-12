# Deployment

`mlxlink_exporter` runs on the host. NVIDIA MFT's `mlxlink` accesses adapter firmware, so container deployment is deliberately unsupported.

## Prerequisites

- Linux with NVIDIA MFT installed and `/usr/bin/mlxlink` available, unless `--mlxlink-path` changes the path.
- Read access to `/sys/class/infiniband`.
- A dedicated unprivileged `mlxlink_exporter` service user. Do not start as root unless the root override is proven necessary.

## Install with systemd

1. Install the binary and create the service user.

   ```bash
   sudo install -Dm0755 mlxlink_exporter /usr/local/bin/mlxlink_exporter
   sudo useradd --system --home /var/lib/mlxlink_exporter --shell /usr/sbin/nologin mlxlink_exporter
   ```

2. Optionally configure the environment file. Every flag other than `--version` has a `MLXLINK_EXPORTER_*` counterpart, so an empty file is valid.

   ```bash
   sudo install -Dm0644 /dev/null /etc/mlxlink_exporter.env
   echo 'MLXLINK_EXPORTER_LISTEN_ADDRESS=:9880' | sudo tee -a /etc/mlxlink_exporter.env
   echo 'MLXLINK_EXPORTER_POLL_INTERVAL=30s' | sudo tee -a /etc/mlxlink_exporter.env
   echo 'MLXLINK_EXPORTER_SHOW_EYE=false' | sudo tee -a /etc/mlxlink_exporter.env
   echo 'MLXLINK_EXPORTER_SHOW_PCIE_EYE=false' | sudo tee -a /etc/mlxlink_exporter.env
   ```

   Keep configuration in the environment file: systemd does not evaluate shell-style defaults in `ExecStart`. The shipped unit starts the binary without flags and relies on its built-in defaults.

3. Install and start the unit.

   ```bash
   sudo install -Dm0644 deploy/systemd/mlxlink_exporter.service /etc/systemd/system/mlxlink_exporter.service
   sudo systemctl daemon-reload
   sudo systemctl enable --now mlxlink_exporter.service
   curl -f http://localhost:9880/healthz
   curl -s http://localhost:9880/metrics | grep '^mlxlink_'
   ```

## Verify privileges

The supplied unit is unprivileged, fully hardened, and has an empty capability bounding set. Start there and inspect `mlxlink_collection_errors_total`.

- `reason="permission_denied"` means the service cannot execute the binary; check its mode and ownership.
- `reason="exit_error"` commonly represents an MFT run-time privilege error. Run the command as the service user to see the vendor message:

  ```bash
  sudo systemctl stop mlxlink_exporter.service
  sudo -u mlxlink_exporter /usr/bin/mlxlink -d mlx5_0 -m -c --json
  ```

Grant the smallest working privilege in this order: device-node access via group/udev, an explicitly required capability, then the supplied root-only override. Use the override only after recording why unprivileged MFT collection cannot work:

```bash
sudo install -Dm0644 deploy/systemd/mlxlink_exporter-root.conf \
  /etc/systemd/system/mlxlink_exporter.service.d/root.conf
sudo systemctl daemon-reload
sudo systemctl restart mlxlink_exporter.service
```

Before enabling either Eye flag, run its exact command as the service user and verify it completes within `MLXLINK_EXPORTER_COMMAND_TIMEOUT`, returns `status.code: 0`, and exposes the expected Eye section:

```bash
sudo -u mlxlink_exporter /usr/bin/mlxlink -d mlx5_0 -m -c \
  --rx_fec_histogram --show_histogram --show_serdes_tx --show_eye --json
sudo -u mlxlink_exporter /usr/bin/mlxlink -d mlx5_0 \
  --port_type PCIE --show_eye --json
```

If a command does not succeed here, leave the corresponding flag disabled rather than enabling it and letting the exporter fall back: the fallback runs again on every sweep, as described below.

## Operational behavior

- `/healthz` is liveness; `/readyz` returns 503 until a device has been collected and remains 503 on hosts with no RDMA devices.
- Scrapes read an immutable cache. Only `--poll-interval` controls `mlxlink` execution frequency.
- Network Eye and PCIe Eye default to disabled. PCIe Eye runs after network collection and failures are isolated from network snapshots, readiness, and `mlxlink_collector_up`.
- A rejected query is retried every sweep. The fallback is decided per device per sweep and nothing is remembered between sweeps, so on a host where `--show_eye` always exits non-zero every sweep runs `mlxlink` twice per device instead of once, or three times if the `--rx_fec_histogram --show_histogram --show_serdes_tx` extras are rejected as well. Treat a steadily rising `mlxlink_collection_errors_total{reason="exit_error"}` while an Eye flag is enabled — with `mlxlink_collector_up` still 1 — as "this query is not supported here": disable the flag. The exporter already publishes what the fallback returns, so the extra invocations buy nothing and lengthen the sweep.
- Watch `mlxlink_sweep_overlaps_total`. It counts ticks dropped because the previous sweep was still running, so any sustained increase means the sweep does not fit inside `--poll-interval` and the interval must be raised (or devices excluded).
- Update this guide and the systemd assets whenever a new flag or metric changes deployment behavior.

