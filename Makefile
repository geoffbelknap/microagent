UNAME_S := $(shell uname -s)
TMPDIR ?= /tmp
PREFIX ?= $(HOME)/.local
INSTALL_KERNEL ?= 1
DOWNLOAD_FIRECRACKER ?= 1
FIRECRACKER_VERSION ?= v1.16.0
FIRECRACKER_SHA256 ?=
INSTALL_HOST_PACKAGES ?= 1
CHECK ?= 1
QUIET ?= 1
ARCH ?=
FIRECRACKER ?=
GOLANGCI_LINT_CACHE ?= $(TMPDIR)/microagent-golangci-lint-cache

INSTALL_ARGS := --prefix $(PREFIX)
ifneq ($(ARCH),)
INSTALL_ARGS += --arch $(ARCH)
endif
ifneq ($(FIRECRACKER),)
INSTALL_ARGS += --firecracker $(FIRECRACKER)
else ifeq ($(DOWNLOAD_FIRECRACKER),0)
INSTALL_ARGS += --no-download-firecracker
else
INSTALL_ARGS += --download-firecracker --firecracker-version $(FIRECRACKER_VERSION)
ifneq ($(FIRECRACKER_SHA256),)
INSTALL_ARGS += --firecracker-sha256 $(FIRECRACKER_SHA256)
endif
endif
ifneq ($(INSTALL_KERNEL),0)
INSTALL_ARGS += --install-kernel
endif
ifneq ($(INSTALL_HOST_PACKAGES),0)
INSTALL_ARGS += --install-host-packages
else
INSTALL_ARGS += --no-install-host-packages
endif
ifeq ($(CHECK),0)
INSTALL_ARGS += --no-check
endif
ifeq ($(QUIET),1)
INSTALL_ARGS += --quiet
endif

.PHONY: fmt lint test-race test dev dev-build install smoke smoke-contract smoke-rootfs smoke-microagent-e2e smoke-microagent-public-surface smoke-microagent-lifecycle-matrix smoke-microagent-networking smoke-microagent-mediation smoke-microagent-supervise smoke-firecracker smoke-firecracker-console smoke-firecracker-publish smoke-firecracker-network smoke-firecracker-confined smoke-workspace smoke-applevf-network smoke-applevf-publish smoke-applevf-vsock release-check release-check-live signed-supervisor smoke-boot

fmt:
	gofmt -w cmd pkg supervisors

lint:
	GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)" golangci-lint run

test-race:
	go test -race ./...

test:
	go test ./...
ifeq ($(UNAME_S),Darwin)
	swift build --package-path supervisors/applevf --disable-sandbox
endif

dev-build:
	scripts/dev/build-local.sh

dev:
	@scripts/dev/dev.sh

install:
	@scripts/install-from-source.sh $(INSTALL_ARGS)

smoke: test
ifeq ($(UNAME_S),Darwin)
	scripts/dev/microagent-e2e.sh contract
	scripts/dev/applevf-supervisor-build.sh
	scripts/dev/applevf-supervisor-lifecycle-smoke.sh
	scripts/dev/cli-lifecycle-smoke.sh
	scripts/dev/cli-workspace-smoke.sh
	scripts/dev/applevf-network-mode-smoke.sh
	scripts/dev/applevf-publish-smoke.sh
	scripts/dev/applevf-workspace-connect-smoke.sh
	scripts/dev/applevf-live-boot-smoke.sh
else ifeq ($(UNAME_S),Linux)
	scripts/dev/microagent-e2e.sh
else
	@echo "smoke is not supported on $(UNAME_S)" >&2
	@exit 2
endif

smoke-contract:
	scripts/dev/microagent-e2e.sh contract

smoke-rootfs:
	scripts/dev/rootfs-oci-smoke.sh

smoke-microagent-e2e:
	scripts/dev/microagent-e2e.sh

smoke-microagent-public-surface:
	scripts/dev/microagent-e2e.sh public-surface

smoke-microagent-lifecycle-matrix:
	scripts/dev/microagent-e2e.sh lifecycle-matrix

smoke-microagent-networking:
	scripts/dev/microagent-e2e.sh networking

smoke-microagent-mediation:
	scripts/dev/microagent-e2e.sh mediation

smoke-microagent-supervise:
	scripts/dev/microagent-e2e.sh supervision

smoke-firecracker:
	scripts/dev/microagent-e2e.sh public-surface lifecycle-matrix networking

smoke-firecracker-console:
	scripts/dev/microagent-e2e.sh lifecycle-matrix

smoke-firecracker-publish:
	scripts/dev/microagent-e2e.sh networking

smoke-firecracker-network:
	scripts/dev/microagent-e2e.sh networking

smoke-firecracker-confined:
	scripts/dev/firecracker-confined-smoke.sh

smoke-workspace:
ifeq ($(UNAME_S),Darwin)
	scripts/dev/applevf-supervisor-build.sh
	scripts/dev/applevf-workspace-connect-smoke.sh
else ifeq ($(UNAME_S),Linux)
	scripts/dev/microagent-e2e.sh lifecycle-matrix
else
	@echo "workspace smoke is not supported on $(UNAME_S)" >&2
	@exit 2
endif

smoke-applevf-network: signed-supervisor
	scripts/dev/applevf-network-mode-smoke.sh

smoke-applevf-publish: signed-supervisor
	scripts/dev/applevf-publish-smoke.sh

smoke-applevf-vsock: signed-supervisor
	scripts/dev/applevf-vsock-diagnostic-smoke.sh

release-check:
	scripts/dev/release-check.sh

release-check-live:
	scripts/dev/release-check.sh --live

signed-supervisor:
	scripts/dev/applevf-supervisor-build.sh

smoke-boot: signed-supervisor
	scripts/dev/applevf-live-boot-smoke.sh
