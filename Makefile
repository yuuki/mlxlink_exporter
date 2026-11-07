GO ?= go
PKG := ./...
BINARY := mlxlink_exporter

# Tool versions. CI pins the same versions (.github/workflows); bump together.
GOLANGCI_LINT_VERSION ?= v2.15.0
GOVULNCHECK_VERSION   ?= v1.9.0
ACTIONLINT_VERSION    ?= v1.7.12
ZIZMOR_VERSION        ?= 1.32.0
FUZZTIME              ?= 30s
# Go minimises every new interesting input for up to 60s by default, which
# stalls a short run on the multi-kilobyte JSON seeds.
FUZZMINIMIZETIME      ?= 1s
BENCH_COUNT           ?= 6

# The toolchain go.mod selects. Every go-run tool is built with it: golangci-lint
# and govulncheck type-check with the go/types they were built with, so a build
# by an older go on PATH (switching to the tool's own go.mod toolchain) would
# refuse this module.
GO_TOOLCHAIN = $(shell $(GO) env GOVERSION)
GOLANGCI_LINT ?= GOTOOLCHAIN=$(GO_TOOLCHAIN) $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
# uvx or pipx (preinstalled on GitHub-hosted runners) runs the pinned zizmor.
ZIZMOR ?= $(if $(shell command -v uvx 2>/dev/null),uvx zizmor@$(ZIZMOR_VERSION),pipx run zizmor==$(ZIZMOR_VERSION))

.PHONY: all build $(BINARY) check test test-race test-fuzz bench lint lint-actions vet vuln fmt complexity clean

all: build

build: $(BINARY)

# Phony on purpose: go build is incremental, and a file target without
# prerequisites would never rebuild a stale binary.
$(BINARY):
	$(GO) build -o $@ .

# The local pre-PR gate: everything the required CI checks run, none of which
# needs root, RDMA hardware or MFT.
check: lint lint-actions test-race build

test:
	$(GO) test $(PKG)

# What CI runs; the poller tests are concurrency-heavy, so -race is required.
test-race:
	$(GO) test -race $(PKG)

# Run every fuzz target for FUZZTIME. Pass FUZZFLAGS=-parallel=2 on a shared
# machine. A failing input is written to testdata/fuzz/<target>/.
test-fuzz:
	@set -e; for pkg in $$($(GO) list $(PKG)); do \
		targets=$$($(GO) test -list '^Fuzz' $$pkg); \
		for f in $$(printf '%s\n' "$$targets" | sed -n 's/^\(Fuzz[[:alnum:]_]*\)$$/\1/p'); do \
			echo "== $$pkg $$f"; \
			$(GO) test -run='^$$' -fuzz="^$$f\$$" -fuzztime=$(FUZZTIME) \
				-fuzzminimizetime=$(FUZZMINIMIZETIME) $(FUZZFLAGS) $$pkg; \
		done; \
	done

# Benchmarks in benchstat format (bench.txt is gitignored).
bench:
	$(GO) test -run='^$$' -bench=. -benchmem -count=$(BENCH_COUNT) $(PKG) | tee bench.txt

# golangci-lint (linters and formatters per .golangci.yml, go vet included)
# and go.mod tidiness.
lint:
	$(GOLANGCI_LINT) run
	$(GO) mod tidy -diff

# Lint (actionlint) and audit (zizmor) the GitHub Actions workflows. zizmor
# runs its online audits only when GH_TOKEN is set, as in CI.
lint-actions:
	GOTOOLCHAIN=$(GO_TOOLCHAIN) $(GO) run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
	$(ZIZMOR) $(if $(GH_TOKEN),,--offline) .

vet:
	$(GO) vet $(PKG)

# Known vulnerabilities reachable from the code.
vuln:
	GOTOOLCHAIN=$(GO_TOOLCHAIN) $(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) $(PKG)

fmt:
	gofmt -w $(shell find . -type f -name '*.go')

# Rank the most complex functions with cccc (https://github.com/moznion/cccc);
# what is analyzed is configured in cccc.toml.
complexity:
	cccc --table --top-cognitive 20 .

clean:
	rm -f $(BINARY) bench.txt
