BINARY := harnesh
CMD := ./cmd/harnesh
VERSION ?= 0.1.0
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build test vet install clean

all: build

## Build the harnesh binary in the project root.
build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

## Run the full test suite with race detection enabled.
test:
	go test -v -race ./...

## Run static analysis checks.
vet:
	go vet ./...

## Install harnesh into $GOPATH/bin (or $GOBIN).
install:
	go install -ldflags "$(LDFLAGS)" $(CMD)

## Remove compiled binaries.
clean:
	rm -f $(BINARY)
	rm -rf bin dist
