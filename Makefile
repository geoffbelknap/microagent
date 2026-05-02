.PHONY: test smoke smoke-rootfs signed-helper smoke-boot

test:
	go test ./...
	swift build --package-path helpers/applevf --disable-sandbox

smoke: test
	scripts/helper-lifecycle-smoke.sh
	scripts/cli-lifecycle-smoke.sh

smoke-rootfs:
	scripts/rootfs-oci-smoke.sh

signed-helper:
	scripts/applevf-helper-build.sh

smoke-boot: signed-helper
	scripts/applevf-boot-smoke.sh
