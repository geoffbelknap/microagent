# homebridge

This example builds Homebridge in a microVM-native way: start from Ubuntu, run a
setup script, and let `microagent-init` supervise the service command. It does
not use the Homebridge Docker image as a container runtime contract.

The setup script follows the upstream Debian/Ubuntu package install flow from
the Homebridge wiki:

https://github.com/homebridge/homebridge/wiki/Install-Homebridge-on-Debian-or-Ubuntu-Linux

## Files

| File | Role |
|---|---|
| `microagent.yaml` | Workspace recipe: base image, setup file, service, resources, and port forward. |
| `setup.sh` | Guest setup script that installs the Homebridge apt package. |
| `start.sh` | Foreground service wrapper copied into the guest. |

## Run

From the repo root:

```sh
make dev-build

.build/dev/microagent create --file examples/homebridge/microagent.yaml
.build/dev/microagent start homebridge
curl -I http://127.0.0.1:8581/
```

The Homebridge UI should be available at:

```text
http://127.0.0.1:8581/
```

To inspect the guest:

```sh
.build/dev/microagent connect homebridge
```

## Notes

- The Debian/Ubuntu package's `/usr/local/bin/hb-service` wrapper expects
  `systemd`. The recipe runs the underlying Homebridge UI service helper in the
  foreground instead, so `microagent-init` can supervise it directly.
- Homebridge writes logs to `/var/lib/homebridge/homebridge.log`, which is the
  path the Homebridge UI expects for its native log viewer.
- Bonjour/mDNS discovery is not expected to work through Apple VF NAT.
- The Homebridge web UI can still be useful for plugin and config management,
  but HomeKit pairing/discovery may need additional host-network support that
  microagent does not provide today.
