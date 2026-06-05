#!/bin/sh
set -e
mkdir -p /etc/dansal /var/lib/dansal-web
[ -f /etc/dansal/web.yaml ] || cp /defaults/web.yaml /etc/dansal/web.yaml
exec "$@"
