UNAME_S := $(shell uname -s)

.PHONY: test smoke smoke-rootfs smoke-firecracker smoke-workspace release-check signed-supervisor smoke-boot

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
	scripts/applevf-workspace-connect-smoke.sh
else ifeq ($(UNAME_S),Linux)
	scripts/firecracker-workspace-smoke.sh
	scripts/firecracker-boot-smoke.sh
else
	@echo "smoke is not supported on $(UNAME_S)" >&2
	@exit 2
endif

smoke-rootfs:
	scripts/rootfs-oci-smoke.sh

smoke-firecracker:
	scripts/firecracker-boot-smoke.sh

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

release-check: test smoke smoke-rootfs

signed-supervisor:
	scripts/applevf-supervisor-build.sh

smoke-boot: signed-supervisor
	scripts/applevf-boot-smoke.sh
