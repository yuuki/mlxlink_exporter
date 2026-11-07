# Contributing

Thanks for helping with mlxlink_exporter.

- **Discuss first** for anything beyond a small fix: open an issue describing
  the problem, the MFT version and adapter involved, and what you expect.
- **Read [docs/design.md](docs/design.md)** before changing how collection
  works; it explains why the exporter behaves as it does.
- **Keep pull requests focused.** One change per PR, with tests for any
  behaviour change. When MFT output changes, add the real captured response
  (serial numbers redacted) as a fixture rather than editing an existing one.
- **Run `make check`** before pushing. It runs the linters, the workflow
  audit and the race-enabled test suite, and needs neither root nor RDMA
  hardware. Touching the decoder or discovery? Run `make test-fuzz` too.
- **Keep the docs in sync.** A change to metrics, flags or failure behaviour
  updates `docs/design.md`, `README.md` and `docs/deployment.md` together.
- **Commit style:** short scoped subjects such as `mlxlink: …`, `docs: …`,
  `release: …`, `ci: …`, `deps: …`, with the reason in the body.

The full command list, conventions and compatibility contracts are in
[AGENTS.md](AGENTS.md). Security issues go through [SECURITY.md](SECURITY.md),
not public issues.
