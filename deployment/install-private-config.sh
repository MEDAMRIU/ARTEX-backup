#!/bin/sh
set -eu
test "$(id -u)" -eq 0
private=/root/artex-install/private
test -s "$private/config/app.env"
test -s "$private/auth/jwt.key"
install -m 600 "$private/config/app.env" /etc/artex/app.env
install -m 600 "$private/config/database.env" /etc/artex/database.env
cp -a "$private/auth/." /var/lib/artex/auth/
chown -R artex:artex /var/lib/artex/auth
chmod 700 /var/lib/artex/auth
runtime=/var/lib/artex/runtime/task-1
install -d -m 700 -o artex -g artex /var/lib/artex/runtime "$runtime" "$runtime/broker" "$runtime/secrets" /var/lib/artex/data/tasks /var/lib/artex/data/tasks/1
install -d -m 755 "$runtime/config"
for name in policy runner broker; do
  install -m 644 "$private/config/$name.json" "$runtime/config/$name.json"
done
install -m 600 -o artex -g artex "$private/config/runner-token" "$runtime/secrets/runner-token"
test ! -e "$runtime/data"
ln -s /var/lib/artex/data "$runtime/data"
docker compose --env-file /etc/artex/database.env -p artex-control -f /opt/artex/current/deployment/database.yml up -d --wait
systemctl enable --now artex.service
systemctl is-active artex.service
