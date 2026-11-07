# AGENTS.md

Guidance for coding agents (and humans) working in this repository. `CLAUDE.md` imports this file.

Prometheus exporter for NVIDIA MFT's `mlxlink`. `docs/design.md` is the source of truth for rationale, the error taxonomy and known limitations; read it before changing collection behaviour. User-facing documentation (flags, metrics, operational notes) lives in `README.md`, the systemd procedure in `docs/deployment.md`.

## 1. Architecture

`main.go` wires: discovery (sysfs) → runner (exec `mlxlink`) → decoder (JSON → `PortData`) → poller (background sweep) → `atomic.Pointer` snapshot → collector (`/metrics`).

- **Discovery** walks `<sysfs-root>/class/infiniband`, skips SR-IOV VFs, excluded devices and names that cannot be label values, and picks the lowest numeric port of each device.
- **Runner** executes one of four fixed argument vectors per device (`-d <device> … --json`) with `LC_ALL=C`, bounded output (4 MiB stdout, 4 KiB stderr) and a `--command-timeout` deadline plus an equal `WaitDelay`.
- **Decoder** turns `result.output.<section>` into `PortData`/`PCIeEye`; MFT key spellings are in `fieldAliases`.
- **Poller** runs one goroutine and one sweep at a time, with the staged fallback (Eye → combined → baseline) on non-zero exits only, and publishes an immutable snapshot set after each device.
- **Collectors** read the snapshot only. Network and root-PCIe Eye telemetry travel through separate stores, collectors and error counters; the PCIe ones are registered only under `--show-pcie-eye`.
- **Server** (`internal/mlxlinkexporter`) serves `/metrics`, `/healthz` (always 200) and `/readyz` (503 until one device was collected) on `:9880`.

## 2. Directory layout

| Path | Contents |
| ---- | -------- |
| `main.go` | Flag parsing, wiring, signal handling and the shutdown order |
| `internal/mlxlink/` | `discovery.go`, `runner.go`, `decoder.go`, `poller.go`, `collector.go`, `pcie_eye_collector.go`, `model.go` (types, `ErrorReason`) |
| `internal/mlxlink/testdata/mlxlink/` | Real MFT 4.34.1 captures and fixtures derived from them |
| `internal/mlxlink/testdata/sysfs/` | Fixture sysfs trees (`basic`, `multiport`, `vf`) |
| `internal/mlxlink/testdata/fuzz/` | Fuzz regression inputs (committed crashers) |
| `internal/mlxlinkexporter/` | `config.go` (flags and `MLXLINK_EXPORTER_*` fallbacks), `server.go` (HTTP) |
| `deploy/systemd/` | Shipped unit, root override, and tests asserting on both |
| `docs/` | `design.md` (source of truth), `deployment.md` |
| `.goreleaser.yaml`, `Dockerfile` | Release archives and the exporter-only container image |
| `.github/workflows/` | `ci.yml`, `security.yml`, `release.yml`, `complexity.yml` |
| `cccc.toml`, `.octocov.complexity.yml` | Complexity measurement and its tracking |

## 3. Build

```bash
make build          # go build -o mlxlink_exporter . (pure Go, no cgo)
goreleaser check    # release config; CI validates it on every push
```

- The Go version is pinned in both `mise.toml` and `go.mod`; bump them together. Dependabot does not bump either.
- The target is Linux only (GoReleaser builds linux/amd64 and linux/arm64 with `CGO_ENABLED=0`), but everything builds and tests on macOS.

## 4. Test

```bash
make test           # go test ./...
make test-race      # go test -race ./... — what CI runs; -race is required, these tests are concurrency-heavy
go test -race ./internal/mlxlink -run TestPoller_PartialSweepStaysVisible  # single test
make test-fuzz FUZZTIME=30s FUZZFLAGS=-parallel=2   # every fuzz target in turn
go test ./internal/mlxlink -run '^$' -fuzz '^FuzzDecode$' -fuzztime 30s -fuzzminimizetime 1s  # one target
make bench          # benchmarks in benchstat format into bench.txt (BENCH_COUNT=6)
```

- No test needs root, RDMA hardware, MFT, Docker or network services: sysfs comes from fixture trees and `mlxlink` from generated `/bin/sh` scripts (which use `sleep`; one test also creates a FIFO). There are no integration or e2e suites.
- `goleak.VerifyTestMain` runs in `internal/mlxlink` and `internal/mlxlinkexporter`: a test that leaves a goroutine behind fails the package. Fix the test, not the check.
- Fuzz targets (`internal/mlxlink/*_fuzz_test.go`) state properties, not just "no panic": `FuzzDecode` (mutated captures: envelope verdict, determinism, decoder invariants, exported series match decoded values one to one), `FuzzDecodeEnvelope` (fails exactly for a non-zero status or a non-object `result.output`), `FuzzDecodeRenamedKey` (a renamed key changes only its own section's family), `FuzzOptionalSectionsRoundTrip` and `FuzzLaneFamiliesRoundTrip` (vendor-shaped encode → decode identity, unreadable lanes dropped per the lane rules), `FuzzSysfsDiscovery` (every discovered target is exportable), `FuzzLimitedBuffer` (bounded output). A crasher lands in `testdata/fuzz/<target>/`; keep it there as a regression seed once fixed.
- Keep `FUZZMINIMIZETIME` short (default 1s): Go's 60s default minimisation stalls short runs on the multi-kilobyte JSON seeds.

## 5. Static analysis

```bash
make lint           # golangci-lint v2.15.0 per .golangci.yml (go vet, staticcheck, gosec, …, gofmt) + go mod tidy -diff
make vet            # plain go vet ./...
make fmt            # gofmt -w over all .go files
make lint-actions   # actionlint + zizmor on .github/ (zizmor --offline unless GH_TOKEN is set)
make vuln           # govulncheck ./...
make complexity     # cccc complexity ranking (needs cccc; CI tracks the scores with octocov)
```

- Tool versions live at the top of the `Makefile`; CI pins the same ones (golangci-lint `version:` in `ci.yml`). Bump them together.
- Tools run with `go run` (golangci-lint, govulncheck, actionlint) are built with the toolchain `go.mod` selects (`GOTOOLCHAIN=$(go env GOVERSION)`): a build by an older Go on PATH cannot type-check this module.
- With a `GH_TOKEN` set but no GitHub API access, run `env -u GH_TOKEN make check` (or `make lint-actions`) for the offline audits; `make … GH_TOKEN=` does not work because make exports the empty value. CI sets `GH_TOKEN` on purpose so the online audits run there.
- CodeQL (Go, default queries) and govulncheck run in `.github/workflows/security.yml`.
- A `//nolint:<linter> // <reason>` needs the reason; a `# zizmor: ignore[rule]` needs it on the same line.

## 6. Coding conventions

- MFT key spellings live in `fieldAliases` (`decoder.go`); the FEC, SerDes and Eye parsers own their structural keys. When MFT output changes, fix those and **add a real captured fixture** under `internal/mlxlink/testdata/mlxlink/` with serial numbers redacted. Never branch on MFT versions.
- Unit normalisation belongs to the decoder, not the collector, and only where `mlxlink` uses a milli prefix.
- `Decode` returns plain errors; turning a failure into an `ErrorReason` is the caller's job. A cancelled parent context is returned as-is, never as a collection error.
- Poller tests use a fake clock, ticker, discovery and runner, and synchronise on events the poller emits rather than on sleeps. Collector tests compare exposition text and run `promlint`. `deploy/systemd/*_test.go` asserts on the shipped unit files, so unit-file edits go through those tests.
- A new flag needs its `MLXLINK_EXPORTER_*` fallback in `config.go`, a README flag-table row, and a `config_test.go` case.
- Logging uses `log/slog`; tool output (stderr) goes to debug logs only.
- Comments say why, not what; match the existing density. Code, comments and docs are in English.
- Commits use short scoped subjects (`mlxlink:`, `docs:`, `release:`, `ci:`, `deps:`) with the reason in the body.

## 7. Compatibility contracts

Changing any of these is a user-visible change: call it out in the PR and update the docs listed in §10.

| Contract | Defined in | Documented in |
| -------- | ---------- | ------------- |
| Metric names, types, help and label sets | `collector.go`, `pcie_eye_collector.go`, `poller.go` (error and overlap counters) | README "Metrics", design §5.5, §6 |
| `reason` label values (`ErrorReason`, a closed set) | `model.go`, `runner.go` | design §6, README |
| Flags and `MLXLINK_EXPORTER_*` variables, defaults | `internal/mlxlinkexporter/config.go` | README "Configuration", `deploy/systemd/mlxlink_exporter.service`, `docs/deployment.md` |
| HTTP endpoints and default listen address `:9880` | `config.go`, `server.go` | README, design §2 |
| `mlxlink` argument vectors | `runner.go` | design §5.2, `docs/deployment.md` |
| Accepted MFT JSON shapes and key spellings | `decoder.go` (`fieldAliases`, structural parsers) | design §5.3 |
| Release archives, checksums file, image `ghcr.io/yuuki/mlxlink_exporter` tags | `.goreleaser.yaml`, `Dockerfile` | README "Install" |
| Shipped systemd unit and root override | `deploy/systemd/` | `docs/deployment.md` |

## 8. Concurrency and resource management

Breaking one of these invariants is a behavioural regression even if the tests still compile.

- **A scrape executes nothing.** `Collect` reads a snapshot pointer: no exec, no sysfs, no locks. Descriptors are built in the constructor.
- **One `mlxlink` process at a time.** Concurrency of one is structural, not lock-enforced; a tick arriving mid-sweep is dropped and counted.
- **Missing is never zero.** `Value.Valid == false` omits the series. Lane flag families and the FEC histogram are all-or-nothing.
- **Only an untrustworthy response fails decoding** (malformed JSON, non-zero `status.code`, absent `result.output`). A renamed field costs that field, not the whole device.
- **`ErrorReason` is a closed, stable set.** Its values are the `reason` label, so adding one changes the public metric contract.
- **Never build an `mlxlink` command from input.** Fixed argument vectors, direct exec (never a shell), `LC_ALL=C`. Tool output goes to the debug log, never into a label.
- **Failure keeps serving.** A failed device retains its previous data until the staleness horizon (`--poll-interval` × 5). Nothing makes the process exit or a scrape fail.

Ownership: `main` starts the poller goroutine and the HTTP server goroutine; on a signal or a server error it cancels the context, waits for `Poller.Run` to return (which also ends any in-flight `mlxlink`), then shuts the server down. Published snapshot sets are immutable and replaced wholesale (copy-on-write); never mutate one after `Store`. Every label value must be valid UTF-8 (Prometheus panics on anything else), which is why discovery rejects names that are not.

## 9. Performance constraints

- One `mlxlink` invocation costs about 0.8 s of firmware access per device; a sweep visits devices sequentially, so its length is N × 0.8 s plus any fallback or PCIe Eye invocations, and must fit inside `--poll-interval`.
- A scrape is a cache read: `BenchmarkMlxlinkCollectorCollect` (8 devices × 8 lanes, ~1,200 series) takes about 1 ms against a 50 ms budget (design §9). Keep work out of `Collect`; `BenchmarkDecode` covers the per-device decode of each sweep.
- Label cardinality is bounded by devices × lanes × fixed enumerations. Never put free-form tool output into a label (the module serial number in `mlxlink_module_info` is the documented exception).

## 10. Before opening a PR

- `make check` (lint, lint-actions, test-race, build) must pass.
- Touching the decoder, discovery or the runner's output buffer: also `make test-fuzz FUZZTIME=30s`.
- New MFT output shape: add the real capture as a fixture (serials redacted) and a golden test.
- Touching `.goreleaser.yaml` or the `Dockerfile`: `goreleaser check` (and `goreleaser release --snapshot --clean` to build the image locally).
- Keep in sync: a change to metrics, flags or failure behaviour is not finished until `docs/design.md`, `README.md` (flag table, metric list, operational notes) and `docs/deployment.md` agree.

## 11. Rules for AI agents

Forbidden:

- Disabling, skipping, deleting or weakening tests, fuzz properties, lint rules or CI checks to get green; hiding failures (`|| true`, `continue-on-error`, blanket excludes).
- Editing generated or fixture data by hand to make a test pass (captures are real MFT output; regenerate or add a fixture instead).
- Adding dependencies without a written justification (test-only dependencies included).
- Changing a compatibility contract (§7) without calling it out.
- Loosening workflow permissions, unpinning actions, enabling caches in the release workflow, or adding third-party services or GitHub Apps.
- Committing secrets, rewriting history, force-pushing, tagging or publishing releases.

## 12. Changes that need human review

Even with green CI: workflow and release files (`.github/`, `.goreleaser.yaml`, `Dockerfile`), the systemd unit and its privileges, permissions or secrets, the runner (process execution, timeouts, output bounds), the decoder and discovery (parsers of external input), metric or flag contracts, dependency additions, and large refactors.
