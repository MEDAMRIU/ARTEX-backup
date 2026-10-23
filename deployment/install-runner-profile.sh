#!/bin/sh
set -eu
test "$(id -u)" -eq 0
source_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
apparmor_parser -Q "$source_dir/artex-runner.apparmor"
install -m 644 "$source_dir/artex-runner.apparmor" /etc/apparmor.d/artex-runner
apparmor_parser -r /etc/apparmor.d/artex-runner
