#!/bin/sh
set -eu
# Run as root only for network setup, then permanently drop all capabilities.
# The broker must have a fixed IPv4 on a dedicated, internal Docker network.
case "${ARTEX_BROKER_IP:-}" in ''|*[!0-9.]*) echo 'ARTEX_BROKER_IP is required' >&2; exit 1;; esac
test "$#" -eq 1
runner_uid=${ARTEX_TOOL_UID:-10001}
runner_gid=${ARTEX_TOOL_GID:-10001}
case "$runner_uid:$runner_gid" in *[!0-9:]*|:*|*:) echo 'A non-root tool UID/GID is required' >&2; exit 1;; esac
if ! [ "$runner_uid" -gt 0 ] || ! [ "$runner_gid" -gt 0 ]; then echo 'A non-root tool UID/GID is required' >&2; exit 1; fi
# Host-mounted secrets may belong to the desktop user. Copy only the runner's
# token into its private control directory before dropping root. Tool children
# receive a separate /run tmpfs and cannot see this directory.
mkdir -p /run/artex-control
chmod 700 /run/artex-control
cp -- "$(jq -er .token_file "$1")" /run/artex-control/token
jq '.token_file = "/run/artex-control/token"' "$1" > /run/artex-control/runner.json
chmod 400 /run/artex-control/token /run/artex-control/runner.json
chown -R "$runner_uid:$runner_gid" /run/artex-control
iptables -F OUTPUT
iptables -P OUTPUT DROP
iptables -A OUTPUT -p tcp --dport 8080 -j REJECT
# Docker's 127.0.0.11 resolver forwards outside this namespace. Permit only the
# application loopback address, including after Docker's DNS destination NAT.
iptables -A OUTPUT -o lo -d 127.0.0.1/32 -j ACCEPT
iptables -A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -A OUTPUT -d "$ARTEX_BROKER_IP" -p tcp -m multiport --dports 3128,8081 -j ACCEPT
ip6tables -F OUTPUT
ip6tables -P OUTPUT DROP
ip6tables -A OUTPUT -p tcp --dport 8080 -j REJECT
ip6tables -A OUTPUT -o lo -d ::1/128 -j ACCEPT
ip6tables -A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
printf 'private control plane\n' > /run/artex-control-canary
chmod 600 /run/artex-control-canary
mkdir -p /run/artex-public
curl --noproxy '*' --fail --retry 10 --retry-connrefused --retry-delay 1 --max-time 5 "http://$ARTEX_BROKER_IP:8081/ca.pem" -o /run/artex-public/ca.pem
chmod 644 /run/artex-public/ca.pem
exec setpriv --reuid="$runner_uid" --regid="$runner_gid" --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs /app/artex runtime-runner /run/artex-control/runner.json
