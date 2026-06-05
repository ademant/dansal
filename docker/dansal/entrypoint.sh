#!/bin/sh
set -e
mkdir -p /etc/dansal /var/lib/dansal/images
[ -f /etc/dansal/config.yaml ] || cp /defaults/config.yaml /etc/dansal/config.yaml
exec "$@"
