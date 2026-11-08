.PHONY: test build test-proxy test-isolation test-ctl test-lockhost test-phase0 test-locklog test-pressure go-test go-build go-test-tmux go-get tmux-build test-site
test:
	tests/run.sh
build:
	docker build -t lea-proxy:dev -f proxy/Containerfile .
	docker build -t lea:dev --build-arg LEA_VERSION=dev -f image/Containerfile .
	docker build -t lea-ctl:dev -f image/ctl/Containerfile image/ctl
test-proxy:
	tests/proxy/run.sh
# leadeck.dev (site/): the unit test, then headless Chrome (skipped without one, unless SITE_E2E=required).
test-site:
	python3 -m unittest discover -s tests/site -p 'test_*.py'
	node tests/site/e2e.mjs
test-lockhost:
	tests/integration/lockhost.sh
# The phase-0 exit demo with real consoles (tests/integration/phase0.sh); needs make build, build/lea, cosign.
test-phase0:
	tests/integration/phase0.sh
# lea#324: a lea agent's masked locklog_count and approved locklog_unmask under lox run
# (tests/integration/locklog.sh); needs make build, build/lea, cosign, sudo.
test-locklog:
	tests/integration/locklog.sh
test-isolation:
	tests/integration/isolation.sh
# lea-ctl (#165): lea in its control container on the host's engine; needs make build, build/lea.
test-ctl:
	tests/integration/ctl.sh
# The host-resource e2e (#132) on the host's engine (ENGINE=podman for rootless Podman), each on
# small real containers, two minutes at most: admission under a real load (a 64 MiB, half-CPU
# container), the limits a running container from an older lea gets at the next lea and an OOM
# kept inside it, and the CPU budget of heavy jobs (6 agents' burners, 2 slots, 2 CPUs). Runs on
# the host, not in a container: the daemon reads the container's cgroup from the host.
ENGINE ?= docker
test-pressure:
	$(ENGINE) pull -q docker.io/library/alpine:3.22 >/dev/null
	LEA_E2E_PRESSURE=$(ENGINE) go test -count=1 -run 'TestPressureE2E|TestLimitsE2E|TestHeavyBudgetE2E' -v ./internal/daemon/ ./internal/launcher/ ./cmd/lea/
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
