UNAME_S := $(shell uname -s)

.PHONY: test smoke smoke-rootfs signed-helper smoke-boot

test:
	go test ./...
ifeq ($(UNAME_S),Darwin)
	swift build --package-path helpers/applevf --disable-sandbox
endif

smoke: test
ifeq ($(UNAME_S),Darwin)
	scripts/helper-lifecycle-smoke.sh
	scripts/cli-lifecycle-smoke.sh
endif
	scripts/cli-workspace-smoke.sh

smoke-rootfs:
	scripts/rootfs-oci-smoke.sh

signed-helper:
	scripts/applevf-helper-build.sh

smoke-boot: signed-helper
	scripts/applevf-boot-smoke.sh
