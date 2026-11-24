BINARY := harnesh
CMD := ./cmd/harnesh
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build test vet fmt fmt-check check install clean

all: build

## Build the harnesh binary in the project root.
build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

## Run the full test suite with race detection enabled.
test:
	go test -race ./...

## Run static analysis checks.
vet:
	go vet ./...

## Format all Go sources.
fmt:
	gofmt -w .

## Fail if any Go source is not gofmt-formatted.
fmt-check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "These files need gofmt:"; echo "$$unformatted"; exit 1; \
	fi

## Run everything CI runs.
check: fmt-check vet test

## Install harnesh into $GOPATH/bin (or $GOBIN).
install:
	go install -ldflags "$(LDFLAGS)" $(CMD)

## Remove compiled binaries.
clean:
	rm -f $(BINARY)
	rm -rf bin dist
