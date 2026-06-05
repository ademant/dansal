#!/bin/sh
set -e
mkdir -p /etc/dansal /var/lib/dansal-web
[ -f /etc/dansal/webmin.yaml ] || cp /defaults/webmin.yaml /etc/dansal/webmin.yaml
exec "$@"
