#!/bin/sh
set -eu

export DEBIAN_FRONTEND=noninteractive

apt-get update
apt-get install -y --no-install-recommends ca-certificates curl gpg

install -d -m 0755 /usr/share/keyrings /etc/apt/sources.list.d /var/lib/homebridge
touch /var/lib/homebridge/homebridge.log

curl -fsSL https://repo.homebridge.io/KEY.gpg \
  | gpg --dearmor -o /usr/share/keyrings/homebridge.gpg

echo "deb [signed-by=/usr/share/keyrings/homebridge.gpg] https://repo.homebridge.io stable main" \
  >/etc/apt/sources.list.d/homebridge.list

apt-get update
apt-get install -y --no-install-recommends homebridge

apt-get clean
rm -rf /var/lib/apt/lists/*
