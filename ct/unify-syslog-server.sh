#!/usr/bin/env bash
# UniFi Syslog Server - Proxmox VE LXC (Debian 13)
# Based on community-scripts/ProxmoxVE ct/debian.sh. Run on the Proxmox host:
#
#   bash -c "$(curl -fsSL https://raw.githubusercontent.com/slisinternetservices/slis-unifi-syslog/main/ct/unify-syslog-server.sh)"
#
# The community-scripts engine fetches install/<app>-install.sh from
# COMMUNITY_SCRIPTS_URL, which we point at this repository.
export COMMUNITY_SCRIPTS_URL="${COMMUNITY_SCRIPTS_URL:-https://raw.githubusercontent.com/slisinternetservices/slis-unifi-syslog/main}"
_cs_boot="${COMMUNITY_SCRIPTS_CORE_DIR:-$(dirname "${BASH_SOURCE[0]}")/../../core}/core/build.func"
source "$_cs_boot" 2>/dev/null || source <(curl -fsSL "${COMMUNITY_SCRIPTS_CORE_URL:-https://raw.githubusercontent.com/community-scripts/core/main}/core/build.func")
# Copyright (c) 2021-2026 tteck / community-scripts ORG (build engine, MIT)
# Author: rhisijmuiden
# License: MIT
# Source: https://github.com/slisinternetservices/slis-unifi-syslog

APP="Unify-Syslog-Server"
var_tags="${var_tags:-logging;unifi;network}"
var_cpu="${var_cpu:-4}"
var_ram="${var_ram:-4096}"
var_disk="${var_disk:-40}"
var_os="${var_os:-debian}"
var_version="${var_version:-13}"
var_arm64="${var_arm64:-yes}"
var_unprivileged="${var_unprivileged:-1}"

header_info "$APP"
variables
color
catch_errors

function update_script() {
  header_info
  check_container_storage
  check_container_resources
  if [[ ! -x /opt/unify-syslog/unify-syslog ]]; then
    msg_error "No ${APP} Installation Found!"
    exit
  fi
  msg_info "Updating OS packages (incl. ClickHouse)"
  apt_update_safe
  $STD apt -y upgrade
  msg_ok "Updated OS packages"
  if [[ -d /etc/clickhouse-server/config.d && ! -L /etc/clickhouse-server/config.d/zz-unify-syslog-managed.xml ]]; then
    msg_info "Letting ${APP} configure cold storage in ClickHouse"
    install -d -m 2750 -o unify-syslog -g clickhouse /var/lib/unify-syslog-ch
    [[ -f /var/lib/unify-syslog-ch/storage.xml ]] || echo '<clickhouse/>' >/var/lib/unify-syslog-ch/storage.xml
    chown unify-syslog:clickhouse /var/lib/unify-syslog-ch/storage.xml
    chmod 0640 /var/lib/unify-syslog-ch/storage.xml
    ln -sfn /var/lib/unify-syslog-ch/storage.xml /etc/clickhouse-server/config.d/zz-unify-syslog-managed.xml
    msg_ok "Cold storage can be configured"
  fi
  msg_info "Updating ${APP}"
  if runuser -u unify-syslog -- /opt/unify-syslog/unify-syslog update; then
    systemctl restart unify-syslog
  fi
  msg_ok "Updated successfully!"
  exit
}

start
build_container
description

msg_ok "Completed successfully!\n"
echo -e "${CREATING}${GN}${APP} setup has been successfully initialized!${CL}"
echo -e "${INFO}${YW} Access it using the following URL:${CL}"
echo -e "${TAB}${GATEWAY}${BGN}http://${IP}:8080${CL}"
echo -e "${INFO}${YW} Send UniFi syslog to:${CL} ${BGN}${IP}:514 (UDP/TCP)${CL}"
echo -e "${INFO}${YW} Admin credentials:${CL} pct exec ${CTID} -- cat /root/unify-syslog.creds"
