#!/usr/bin/env bash
# Runs inside the new LXC (called by the community-scripts build engine).
# Copyright (c) 2021-2026 community-scripts ORG (engine helpers, MIT)
# Author: rhisijmuiden
# License: MIT
# Source: https://github.com/slisinternetservices/slis-unifi-syslog

source /dev/stdin <<<"$FUNCTIONS_FILE_PATH"
color
verb_ip6
catch_errors
setting_up_container
network_check
update_os

msg_info "Installing UniFi Syslog probe"
curl -fsSL "https://raw.githubusercontent.com/slisinternetservices/slis-unifi-syslog/main/deploy/install-probe.sh" -o /tmp/uss-install-probe.sh
USS_NONINTERACTIVE=1 USS_PROBE_KEY="${USS_PROBE_KEY:-}" $STD bash /tmp/uss-install-probe.sh
rm -f /tmp/uss-install-probe.sh
msg_ok "Installed UniFi Syslog probe"

motd_ssh
customize
cleanup_lxc
