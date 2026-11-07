# AGENTS.md

This file holds only what the code does not show. [docs/design.md](docs/design.md)
is the source of truth for rationale, the error taxonomy and known
limitations: read it before changing collection behaviour.

## Rules the code does not show

- A scrape executes nothing: `Collect` reads a snapshot pointer (no exec, no
  sysfs, no locks). Published snapshot sets are never mutated after `Store`.
- One `mlxlink` process at a time, structurally (not by a lock); a tick
  arriving mid-sweep is dropped and counted.
- Missing is never zero: an invalid `Value` omits the series; lane flag
  families and the FEC histogram are all-or-nothing.
- Only an untrustworthy response fails decoding (malformed JSON, non-zero
  `status.code`, absent `result.output`); a renamed field costs only itself.
- `ErrorReason` is a closed, stable set: its values are the `reason` label.
- Never build an `mlxlink` command from input: fixed argument vectors, direct
  exec (never a shell), `LC_ALL=C`. Tool output goes to the debug log, never
  into a label (the module serial of `mlxlink_module_info` excepted).
- Failure keeps serving: a failed device keeps its data until the staleness
  horizon; nothing makes the process exit or a scrape fail.
- Fixtures in `internal/mlxlink/testdata/mlxlink/` are real MFT captures
  (serials redacted): add a new capture, never edit one to pass a test, and
  never branch on MFT versions.
- A new flag needs its `MLXLINK_EXPORTER_*` fallback in `config.go`, a README
  flag-table row and a `config_test.go` case.
- English for code, comments, docs and commit messages.

## Compatibility contracts

Call any change out in the pull request.

- Metric names, types, help and label sets: `internal/mlxlink/collector.go`,
  `pcie_eye_collector.go`, `poller.go`.
- `reason` label values (`ErrorReason`): `model.go`, `runner.go`.
- Flags, `MLXLINK_EXPORTER_*` variables, defaults, endpoints and `:9880`:
  `internal/mlxlinkexporter/config.go`, `server.go`.
- `mlxlink` argument vectors (`runner.go`) and accepted MFT JSON shapes and
  key spellings (`decoder.go`).
- Release archives, checksums and image tags (`.goreleaser.yaml`,
  `Dockerfile`); the systemd unit and root override (`deploy/systemd/`).

## Before opening a pull request

- `make check`.
- Decoder, discovery or the runner's output buffer: `make test-fuzz
  FUZZTIME=30s`; commit crashers as regression seeds.
- New MFT output shape: the real capture as a fixture and a golden test.
- `.goreleaser.yaml` or `Dockerfile`: `goreleaser check`.
- Metrics, flags or failure behaviour: not finished until `docs/design.md`,
  `README.md` and `docs/deployment.md` agree.

## Git conventions

Branches: `<type>/<issue>-<summary>`, or `<type>/<summary>` without an issue
(`fix/166-connect-errors`, `feat/qp-counters`). `<type>` is a commit type
below; `<summary>` is 2–5 lowercase ASCII words joined by `-`. Branches named
by tools keep their names (`agent/*`, `claude/*`, `copilot/*`,
`dependabot/*`). Never push to the default branch.

Commits follow [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/):

    <type>(<scope>)!: <subject>

    <body>

    <footer>

- `type`: `feat` (new user-visible behaviour), `fix` (bug fix), `perf`,
  `refactor` (no behaviour change), `test`, `docs`, `build` (build system,
  dependencies, packaging), `ci` (workflows), `chore` (anything else, e.g.
  `chore(release): prepare v1.2.3`), `revert`.
- `scope`: optional; the package or area changed: `mlxlink`, `decoder`,
  `runner`, `poller`, `discovery`, `collector`, `server`, `systemd`.
- `!` and a `BREAKING CHANGE: <what changed and how to migrate>` footer for
  any change to a compatibility contract above.
- `subject`: imperative, lower case (except identifiers), no final period,
  at most 72 characters including the prefix.
- `body`: why the change is needed and what it affects, wrapped at 72
  columns; required unless the subject says everything.
- `footer`: `Fixes #N` / `Refs #N`, `BREAKING CHANGE:`, trailers such as
  `Co-Authored-By:`.
- One logical change per commit; regenerated files go in the commit that
  changes their source. Pull request titles use the same format (they become
  the squash commit).

## Forbidden

- Disabling, skipping, deleting or weakening tests, fuzz properties, lint
  rules or CI checks to get green; hiding failures (`|| true`,
  `continue-on-error`, blanket excludes). A `//nolint:<linter> // <reason>`
  or `# zizmor: ignore[rule]` carries its reason on the same line.
- Adding dependencies, test-only ones included, without a written
  justification.
- Changing a compatibility contract without calling it out.
- Loosening workflow permissions, unpinning actions, enabling caches in the
  release workflow, or adding third-party services or GitHub Apps.
- Committing secrets, rewriting history, force-pushing, tagging or publishing
  releases.

## Needs human review

Even with green CI: `.github/`, `.goreleaser.yaml` and `Dockerfile`; the
systemd unit and its privileges; permissions or secrets; the runner (process
execution, timeouts, output bounds); the decoder and discovery (parsers of
external input); metric or flag contracts; dependency additions; large
refactors.

## Environment notes

- With `GH_TOKEN` set but the GitHub API unreachable, run
  `env -u GH_TOKEN make check`; `make check GH_TOKEN=` does not work because
  make exports the empty value.
