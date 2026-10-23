#!/bin/sh
# Installs a reviewed release on the dedicated host. Secrets are provisioned
# separately under /etc/artex and /var/lib/artex; never in the image/context.
set -eu
test "$(id -u)" -eq 0
release=$(realpath "$1")
case "$release" in /opt/artex/releases/*) ;; *) echo 'Expected a versioned ARTEX release' >&2; exit 1;; esac
if ! getent group artex >/dev/null; then groupadd --gid 10001 artex; fi
if ! id artex >/dev/null 2>&1; then useradd --uid 10001 --gid artex --home-dir /var/lib/artex --shell /usr/sbin/nologin artex; fi
test "$(id -u artex)" -eq 10001
test "$(id -g artex)" -eq 10001
install -d -m 700 -o artex -g artex /var/lib/artex /var/lib/artex/data /var/lib/artex/auth /var/lib/artex/skills
install -d -m 700 /etc/artex
install -m 755 "$release/dist/amd64/artex" "$release/artex"
cp -a "$release/skills/." /var/lib/artex/skills/
chown -R artex:artex /var/lib/artex/skills
ln -s "$release" /opt/artex/current.next
mv -Tf /opt/artex/current.next /opt/artex/current
install -m 644 "$release/deployment/artex.service" /etc/systemd/system/artex.service
systemctl daemon-reload
# Start only after the private environment, auth store and DB are in place.
