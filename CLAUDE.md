# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Prometheus exporter for NVIDIA MFT's `mlxlink` (physical link / optical module telemetry).
One `mlxlink` invocation costs ~0.8 s of firmware access, so **a scrape must never execute `mlxlink`**: a background poller sweeps devices on its own schedule and `/metrics` only reads immutable in-memory snapshots. Nearly every design decision follows from that constraint — see `docs/design.md`, which is the source of truth for rationale and known limitations.

## Commands

```bash
make build                 # go build -o mlxlink_exporter .
make test                  # go test ./...
make lint                  # go vet ./...
make fmt                   # gofmt -w over all .go files
go test -race ./...        # what CI runs; race is required, tests are concurrency-heavy
go test -race ./internal/mlxlink -run TestPoller_PartialSweepStaysVisible   # single test
go test -race -run '^$' -bench BenchmarkMlxlinkCollectorCollect ./internal/mlxlink
GOCACHE=$(pwd)/.gocache GOMODCACHE=$(pwd)/.gomodcache go test ./...   # pinned caches (gitignored)
goreleaser check           # release config validation; CI runs this on every push
goreleaser release --snapshot --clean   # local release + image build without publishing
```

Go version is pinned in both `mise.toml` and `go.mod` (keep them in step; CI uses `go-version-file: go.mod`).
The target platform is Linux only, but the whole test suite runs on macOS: discovery is tested against fixture sysfs trees under `internal/mlxlink/testdata/sysfs/`, and the runner against generated shell scripts.

## Architecture

`main.go` wires everything and owns nothing:

```
Discovery (sysfs) → Runner (exec mlxlink) → Decoder (JSON → PortData)
                          ↓
                    Poller (one goroutine, one sweep at a time)
                          ↓ atomic.Pointer swap of an immutable snapshot set
             Collector / PCIeEyeCollector ← promhttp /metrics
```

- `internal/mlxlink` — the whole collection pipeline (`discovery.go`, `runner.go`, `decoder.go`, `poller.go`, `collector.go`, `pcie_eye_collector.go`, types in `model.go`).
- `internal/mlxlinkexporter` — flag/env parsing (`config.go`) and the HTTP server with `/metrics`, `/healthz`, `/readyz` (`server.go`).
- Network telemetry and root-PCIe Eye telemetry travel through **separate** snapshot stores, collectors and error counters. PCIe collectors are registered only when `--show-pcie-eye` is set, and a PCIe failure must never affect network snapshots, readiness or `mlxlink_collector_up`.

## Invariants to preserve

These are load-bearing; breaking one is a behavioural regression even if tests still compile.

- **The scrape path does no I/O.** `Collector.Collect` reads a snapshot pointer — no exec, no sysfs, no locks. All `prometheus.Desc` values are built (and recorded for `Describe`) in the constructor.
- **`mlxlink` runs one process at a time.** Concurrency of one is structural, not lock-enforced. A tick arriving mid-sweep is dropped and counted in `mlxlink_sweep_overlaps_total`.
- **Missing is not zero.** `Value.Valid == false` (from `N/A`, empty, or unparsable input) means the series is omitted, never exported as 0. Lane flag families and the FEC histogram are all-or-nothing.
- **Only an untrustworthy response fails decoding**: malformed JSON, non-zero `status.code`, or absent `result.output`. A renamed section or field must cost only that field, not the whole device.
- **`ErrorReason` is a closed, stable set** (`model.go`) because its values are the `reason` metric label. Adding one changes the public metric contract; document it in `docs/design.md` §6 and the README.
- **Never build an `mlxlink` command from user input.** The four argument vectors are fixed, the binary is executed directly (never via a shell) with `LC_ALL=C`, and the only substituted value is a device name read from a sysfs listing. Tool output (stderr) goes to the debug log, never into a label.
- **Failure keeps serving.** A failed device retains its previous data and last-success timestamp; measurement series drop only past the staleness horizon (`--poll-interval` × 5). Nothing makes the process exit or a scrape fail.

## Conventions

- **MFT key spellings are centralised in `fieldAliases`** (`decoder.go`) for base fields and section names; the FEC, SerDes and Eye parsers own their own structural keys and parameter allowlists. When MFT output changes, fix the table or the parser and **add a real captured fixture** under `internal/mlxlink/testdata/mlxlink/` — do not branch on MFT versions. Redact serial numbers in new captures.
- **Unit normalisation happens in the decoder, not the collector**, and only where `mlxlink` uses a milli prefix (mV → V, mA → A). Values are otherwise exported exactly as reported.
- **Tests are fixture- and fake-driven.** The poller is tested with a fake clock/ticker/discovery/runner and synchronised on events the poller genuinely emits (never on sleeps); collectors are compared against raw exposition text and linted with `promlint`; `deploy/systemd/*_test.go` asserts on the shipped unit files, so unit-file edits go through those tests.
- **Comments explain why, not what** — the existing code carries dense rationale comments (e.g. why `WaitDelay` exists, why `dockers_v2` over `dockers`). Match that density rather than annotating mechanics.
- Logging is `log/slog` only; every new flag needs its `MLXLINK_EXPORTER_*` environment fallback in `config.go`, a row in the README flag table, and coverage in `config_test.go`.

## Documentation to keep in sync

A change to metrics, flags or failure behaviour is not finished until these agree: `docs/design.md` (architecture, error taxonomy, limitations), `README.md` (flag table, metric list, operational notes), `docs/deployment.md` (systemd, privilege verification).
