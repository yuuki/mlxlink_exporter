GO ?= go
PKG := ./...
BINARY := mlxlink_exporter

.PHONY: all build test lint fmt complexity clean

all: build

build: $(BINARY)

$(BINARY):
	$(GO) build -o $@ .

test:
	$(GO) test $(PKG)

lint:
	$(GO) vet $(PKG)

fmt:
	gofmt -w $(shell find . -type f -name '*.go')

# Rank the most complex functions with cccc (https://github.com/moznion/cccc);
# what is analyzed is configured in cccc.toml.
complexity:
	cccc --table --top-cognitive 20 .

clean:
	rm -f $(BINARY)

