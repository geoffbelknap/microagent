UNAME_S := $(shell uname -s)

.PHONY: test smoke smoke-rootfs smoke-firecracker smoke-firecracker-console smoke-firecracker-publish smoke-firecracker-network smoke-workspace smoke-applevf-network smoke-applevf-vsock release-check signed-supervisor smoke-boot

test:
	go test ./...
ifeq ($(UNAME_S),Darwin)
	swift build --package-path supervisors/applevf --disable-sandbox
endif

smoke: test
ifeq ($(UNAME_S),Darwin)
	scripts/applevf-supervisor-build.sh
	scripts/applevf-supervisor-lifecycle-smoke.sh
	scripts/cli-lifecycle-smoke.sh
	scripts/cli-workspace-smoke.sh
	scripts/applevf-network-mode-smoke.sh
	scripts/applevf-workspace-connect-smoke.sh
else ifeq ($(UNAME_S),Linux)
	scripts/firecracker-workspace-smoke.sh
	scripts/firecracker-console-parity-smoke.sh
	scripts/firecracker-publish-smoke.sh
	scripts/firecracker-network-mode-smoke.sh
	scripts/firecracker-boot-smoke.sh
else
	@echo "smoke is not supported on $(UNAME_S)" >&2
	@exit 2
endif

smoke-rootfs:
	scripts/rootfs-oci-smoke.sh

smoke-firecracker:
	scripts/firecracker-boot-smoke.sh

smoke-firecracker-console:
	scripts/firecracker-console-parity-smoke.sh

smoke-firecracker-publish:
	scripts/firecracker-publish-smoke.sh

smoke-firecracker-network:
	scripts/firecracker-network-mode-smoke.sh

smoke-workspace:
ifeq ($(UNAME_S),Darwin)
	scripts/applevf-supervisor-build.sh
	scripts/applevf-workspace-connect-smoke.sh
else ifeq ($(UNAME_S),Linux)
	scripts/firecracker-workspace-smoke.sh
else
	@echo "workspace smoke is not supported on $(UNAME_S)" >&2
	@exit 2
endif

smoke-applevf-network: signed-supervisor
	scripts/applevf-network-mode-smoke.sh

smoke-applevf-vsock: signed-supervisor
	scripts/applevf-vsock-diagnostic-smoke.sh

release-check: test smoke smoke-rootfs

signed-supervisor:
	scripts/applevf-supervisor-build.sh

smoke-boot: signed-supervisor
	scripts/applevf-boot-smoke.sh
