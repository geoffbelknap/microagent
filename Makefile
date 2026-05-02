.PHONY: test smoke smoke-rootfs

test:
	go test ./...
	swift build --package-path helpers/applevf --disable-sandbox

smoke: test
	scripts/helper-lifecycle-smoke.sh
	scripts/cli-lifecycle-smoke.sh

smoke-rootfs:
	scripts/rootfs-oci-smoke.sh
