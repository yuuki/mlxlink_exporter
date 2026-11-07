# AGENTS.md

## Rules
- A scrape executes nothing: `Collect` only reads the snapshot pointer; a published snapshot set is never mutated after `Store`.
- One `mlxlink` process at a time, by structure rather than a lock; a tick arriving mid-sweep is dropped and counted.
- Missing is never zero: an invalid `Value` omits the series; lane flag families and the FEC histogram are all-or-nothing.
- Only an untrustworthy response fails decoding (malformed JSON, non-zero `status.code`, absent `result.output`); a renamed field costs only itself.
- Failure keeps serving: a failed device keeps its data until the staleness horizon; nothing makes the process exit or a scrape fail.
- New MFT output: add its real capture (serials redacted) to `internal/mlxlink/testdata/mlxlink/` with a golden test; never edit captures or branch on versions.
- A new flag needs its `MLXLINK_EXPORTER_*` fallback in `config.go`, a README flag-table row and a `config_test.go` case.

## Compatibility contracts
- Metric names, types, help and label sets: `internal/mlxlink/collector.go`, `pcie_eye_collector.go`, `poller.go`.
- `reason` label values (`ErrorReason`, a closed set): `model.go`, `runner.go`.
- Flags, `MLXLINK_EXPORTER_*` variables, defaults, endpoints and `:9880`: `internal/mlxlinkexporter/config.go`, `server.go`.
- `mlxlink` argument vectors (`runner.go`); accepted MFT JSON shapes and key spellings (`decoder.go`).
- Release archives, checksums and image tags (`.goreleaser.yaml`, `Dockerfile`); the systemd unit and root override (`deploy/systemd/`).

## Before opening a pull request
- `make check`; also `make test-fuzz` for the decoder, discovery or the runner's output buffer; `goreleaser check` for `.goreleaser.yaml` or `Dockerfile`.

## Git conventions
- Branches: `<type>/<issue>-<summary>` (`<type>/<summary>` without an issue); tools' `agent/*`, `claude/*`, `copilot/*`, `dependabot/*` keep their names.
- Commits and PR titles: Conventional Commits 1.0.0, types `feat` `fix` `perf` `refactor` `test` `docs` `build` `ci` `chore` `revert`; scopes: `mlxlink`, `decoder`, `runner`, `poller`, `discovery`, `collector`, `server`, `systemd`.
