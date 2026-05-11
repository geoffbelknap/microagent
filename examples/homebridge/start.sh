#!/bin/sh
set -eu

export PATH="/opt/homebridge/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

cd /var/lib/homebridge
exec /opt/homebridge/bin/node \
  /opt/homebridge/lib/node_modules/homebridge-config-ui-x/dist/bin/hb-service.js \
  run \
  --allow-root \
  --stdout \
  -U /var/lib/homebridge \
  -P /var/lib/homebridge/node_modules \
  --strict-plugin-resolution
