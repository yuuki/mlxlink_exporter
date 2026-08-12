# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Prometheus exporter for NVIDIA MFT's `mlxlink`. `docs/design.md` is the source of truth for rationale, the error taxonomy and known limitations; read it before changing collection behaviour.

## Commands

```bash
make build          # go build -o mlxlink_exporter .
make lint           # go vet ./...
make fmt            # gofmt -w over all .go files
go test -race ./... # what CI runs; -race is required, these tests are concurrency-heavy
go test -race ./internal/mlxlink -run TestPoller_PartialSweepStaysVisible  # single test
goreleaser check    # release config; CI validates it on every push
```

- The Go version is pinned in both `mise.toml` and `go.mod`; bump them together.
- The target is Linux only, but the whole suite runs on macOS: sysfs comes from fixture trees and `mlxlink` from generated shell scripts.

## Architecture

`main.go` wires: discovery (sysfs) → runner (exec `mlxlink`) → decoder (JSON → `PortData`) → poller (background sweep) → `atomic.Pointer` snapshot → collector (`/metrics`).

Network and root-PCIe Eye telemetry travel through separate stores, collectors and error counters; the PCIe ones are registered only under `--show-pcie-eye`.

## Invariants

Breaking one of these is a behavioural regression even if the tests still compile.

- **A scrape executes nothing.** `Collect` reads a snapshot pointer: no exec, no sysfs, no locks. Descriptors are built in the constructor.
- **One `mlxlink` process at a time.** Concurrency of one is structural, not lock-enforced; a tick arriving mid-sweep is dropped and counted.
- **Missing is never zero.** `Value.Valid == false` omits the series. Lane flag families and the FEC histogram are all-or-nothing.
- **Only an untrustworthy response fails decoding** (malformed JSON, non-zero `status.code`, absent `result.output`). A renamed field costs that field, not the whole device.
- **`ErrorReason` is a closed, stable set.** Its values are the `reason` label, so adding one changes the public metric contract.
- **Never build an `mlxlink` command from input.** Fixed argument vectors, direct exec (never a shell), `LC_ALL=C`. Tool output goes to the debug log, never into a label.
- **Failure keeps serving.** A failed device retains its previous data until the staleness horizon (`--poll-interval` × 5). Nothing makes the process exit or a scrape fail.

## Conventions

- MFT key spellings live in `fieldAliases` (`decoder.go`); the FEC, SerDes and Eye parsers own their structural keys. When MFT output changes, fix those and **add a real captured fixture** under `internal/mlxlink/testdata/mlxlink/` with serial numbers redacted. Never branch on MFT versions.
- Unit normalisation belongs to the decoder, not the collector, and only where `mlxlink` uses a milli prefix.
- Poller tests use a fake clock, ticker, discovery and runner, and synchronise on events the poller emits rather than on sleeps. Collector tests compare exposition text and run `promlint`. `deploy/systemd/*_test.go` asserts on the shipped unit files, so unit-file edits go through those tests.
- A new flag needs its `MLXLINK_EXPORTER_*` fallback in `config.go`, a README flag-table row, and a `config_test.go` case.
- Comments say why, not what; match the existing density.
- Commits use short scoped subjects (`mlxlink:`, `docs:`, `release:`) with the reason in the body.

## Keep in sync

A change to metrics, flags or failure behaviour is not finished until `docs/design.md`, `README.md` (flag table, metric list, operational notes) and `docs/deployment.md` agree.
