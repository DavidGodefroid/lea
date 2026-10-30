.PHONY: test build test-proxy test-isolation test-ctl test-lockhost test-hubdeck test-locksql-confine test-update-deps test-pressure go-test go-build go-test-tmux go-get tmux-build
test:
	tests/run.sh
build:
	docker build -t lea-proxy:dev -f proxy/Containerfile .
	docker build -t lea:dev --build-arg LEA_VERSION=dev -f image/Containerfile .
	docker build -t lea-ctl:dev -f image/ctl/Containerfile image/ctl
test-proxy:
	tests/proxy/run.sh
test-lockhost:
	tests/integration/lockhost.sh
# The phase-2 demo, lea's side: lea's deck approves a ticket in a real lockhub (released images).
test-hubdeck:
	tests/integration/hubdeck.sh
test-locksql-confine:
	tests/integration/locksql-confine.sh
test-update-deps:
	tests/integration/update-deps.sh
test-isolation:
	tests/integration/isolation.sh
# lea-ctl (#165): lea in its control container on the host's engine; needs make build, build/lea.
test-ctl:
	tests/integration/ctl.sh
# The admission e2e (#132): a real 64 MiB, half-CPU container under a real load, on the host's
# engine (ENGINE=podman for rootless Podman); a minute at most. Runs on the host, not in a
# container: the daemon reads the container's cgroup from the host.
ENGINE ?= docker
test-pressure:
	$(ENGINE) pull -q docker.io/library/alpine:3.22 >/dev/null
	LEA_E2E_PRESSURE=$(ENGINE) go test -count=1 -run TestPressureE2E -v ./internal/daemon/
GO_IMAGE ?= golang:1.26
GO_TEST_FLAGS ?=
GO_CACHE ?= $(HOME)/.cache/lea-go
VERSION := $(shell git describe --always --dirty 2>/dev/null || echo dev)
GO_RUN = mkdir -p "$(GO_CACHE)" && docker run --rm -u "$$(id -u):$$(id -g)" -e HOME=/tmp \
	-e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod -e CGO_ENABLED=0 \
	-v "$(GO_CACHE):/cache" -v "$(CURDIR):/src" -w /src $(GO_IMAGE)
go-test:
	$(GO_RUN) go test $(GO_TEST_FLAGS) ./...
go-build:
	$(GO_RUN) go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o build/lea ./cmd/lea
go-test-tmux:
	docker build -q -t lea-go-tmux -f tests/go-tmux/Containerfile tests/go-tmux >/dev/null
	$(MAKE) go-test GO_IMAGE=lea-go-tmux GO_TEST_FLAGS="-count=1 -v"
# make go-get PKG="module@version ...": add or update Go dependencies, then tidy go.mod.
go-get:
	$(GO_RUN) sh -c 'go get $(PKG) && go mod tidy'
# Static tmux for hosts without tmux >= 3.2 (see tools/tmux/README.md): build/tmux.
tmux-build:
	docker build -q -t lea-tmux-build -f tools/tmux/Containerfile tools/tmux >/dev/null
	mkdir -p build
	id=$$(docker create lea-tmux-build) && docker cp "$$id:/out/tmux" build/tmux; rc=$$?; docker rm "$$id" >/dev/null; exit $$rc
	build/tmux -V
