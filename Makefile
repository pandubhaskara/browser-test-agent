# browser-test-agent — cross-compiled binaries for the test VMs.
#
# The backend worker picks a binary by the location's OS and arch from
# RADAR_BROWSER_TEST_RUNNER_DIR (this folder or its dist/) and uploads it over
# SFTP when a location is bootstrapped. SHA256SUMS is for operators and the CI
# artifact: verify a copied dist/ before pointing a backend at it.
#
# No make on the machine (Windows)? dist.ps1 produces the same output.
#
#   make build       host binary, for running the tests locally
#   make dist        all three target binaries + SHA256SUMS
#   make test        unit tests
#   make check       vet + test for every target platform
#   make clean

BINARY  := browser-test-agent
PKG     := ./cmd/$(BINARY)
DIST    := dist
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# CGO off keeps the binaries static: a VM's glibc version is not something a
# test runner should have an opinion about.
GO_ENV   := CGO_ENABLED=0
LDFLAGS  := -s -w -X main.Version=$(VERSION)

TARGETS := \
	$(DIST)/$(BINARY)-linux-amd64 \
	$(DIST)/$(BINARY)-linux-arm64 \
	$(DIST)/$(BINARY)-windows-amd64.exe

.PHONY: all build dist test vet check clean
all: dist

build:
	$(GO_ENV) go build -ldflags="$(LDFLAGS)" -o $(DIST)/$(BINARY) $(PKG)

dist: $(TARGETS) $(DIST)/SHA256SUMS

$(DIST)/$(BINARY)-linux-amd64:
	GOOS=linux GOARCH=amd64 $(GO_ENV) go build -ldflags="$(LDFLAGS)" -o $@ $(PKG)

$(DIST)/$(BINARY)-linux-arm64:
	GOOS=linux GOARCH=arm64 $(GO_ENV) go build -ldflags="$(LDFLAGS)" -o $@ $(PKG)

$(DIST)/$(BINARY)-windows-amd64.exe:
	GOOS=windows GOARCH=amd64 $(GO_ENV) go build -ldflags="$(LDFLAGS)" -o $@ $(PKG)

$(DIST)/SHA256SUMS: $(TARGETS)
	cd $(DIST) && sha256sum $(notdir $(TARGETS)) > SHA256SUMS

test:
	go test ./...

vet:
	go vet ./...

# Every platform's build-tagged files have to compile, not just the host's:
# service_windows.go and run_windows.go are never touched by a Linux build.
check: test
	go vet ./...
	GOOS=linux   GOARCH=amd64 $(GO_ENV) go vet ./...
	GOOS=linux   GOARCH=arm64 $(GO_ENV) go vet ./...
	GOOS=windows GOARCH=amd64 $(GO_ENV) go vet ./...

clean:
	rm -rf $(DIST)
