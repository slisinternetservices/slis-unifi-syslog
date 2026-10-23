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

msg_info "Installing UniFi Syslog Server (ClickHouse + app)"
curl -fsSL "https://raw.githubusercontent.com/slisinternetservices/slis-unifi-syslog/main/deploy/install.sh" -o /tmp/uss-install.sh
USS_NONINTERACTIVE=1 $STD bash /tmp/uss-install.sh
rm -f /tmp/uss-install.sh
msg_ok "Installed UniFi Syslog Server"

motd_ssh
customize
cleanup_lxc
