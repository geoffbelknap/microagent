UNAME_S := $(shell uname -s)

.PHONY: test smoke smoke-contract smoke-rootfs smoke-firecracker smoke-firecracker-console smoke-firecracker-publish smoke-firecracker-network smoke-workspace smoke-applevf-network smoke-applevf-publish smoke-applevf-vsock release-check release-check-live signed-supervisor smoke-boot

test:
	go test ./...
ifeq ($(UNAME_S),Darwin)
	swift build --package-path supervisors/applevf --disable-sandbox
endif

smoke: test
	scripts/dev/runtime-contract-smoke.sh
ifeq ($(UNAME_S),Darwin)
	scripts/dev/applevf-supervisor-build.sh
	scripts/dev/applevf-supervisor-lifecycle-smoke.sh
	scripts/dev/cli-lifecycle-smoke.sh
	scripts/dev/cli-workspace-smoke.sh
	scripts/dev/applevf-network-mode-smoke.sh
	scripts/dev/applevf-publish-smoke.sh
	scripts/dev/applevf-workspace-connect-smoke.sh
else ifeq ($(UNAME_S),Linux)
	scripts/dev/firecracker-workspace-smoke.sh
	scripts/dev/firecracker-console-parity-smoke.sh
	scripts/dev/firecracker-publish-smoke.sh
	scripts/dev/firecracker-network-mode-smoke.sh
	scripts/dev/firecracker-boot-smoke.sh
else
	@echo "smoke is not supported on $(UNAME_S)" >&2
	@exit 2
endif

smoke-contract:
	scripts/dev/runtime-contract-smoke.sh

smoke-rootfs:
	scripts/dev/rootfs-oci-smoke.sh

smoke-firecracker:
	scripts/dev/firecracker-boot-smoke.sh

smoke-firecracker-console:
	scripts/dev/firecracker-console-parity-smoke.sh

smoke-firecracker-publish:
	scripts/dev/firecracker-publish-smoke.sh

smoke-firecracker-network:
	scripts/dev/firecracker-network-mode-smoke.sh

smoke-workspace:
ifeq ($(UNAME_S),Darwin)
	scripts/dev/applevf-supervisor-build.sh
	scripts/dev/applevf-workspace-connect-smoke.sh
else ifeq ($(UNAME_S),Linux)
	scripts/dev/firecracker-workspace-smoke.sh
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
	scripts/dev/applevf-boot-smoke.sh
