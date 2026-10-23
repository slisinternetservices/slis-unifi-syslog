#!/usr/bin/env bash
# UniFi Syslog probe - Proxmox VE LXC (Debian 13)
# Based on community-scripts/ProxmoxVE ct/debian.sh. Run on the Proxmox host
# at the remote site, with the enrollment key from the main server:
#
#   USS_PROBE_KEY='uss1_…' bash -c "$(curl -fsSL https://raw.githubusercontent.com/slisinternetservices/slis-unifi-syslog/main/ct/unify-syslog-probe.sh)"
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

APP="Unify-Syslog-Probe"
var_tags="${var_tags:-logging;unifi;network;probe}"
var_cpu="${var_cpu:-1}"
var_ram="${var_ram:-512}"
var_disk="${var_disk:-4}"
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
  if [[ ! -x /opt/unify-syslog-probe/unify-syslog-probe ]]; then
    msg_error "No ${APP} Installation Found!"
    exit
  fi
  msg_info "Updating OS packages"
  apt_update_safe
  $STD apt -y upgrade
  msg_ok "Updated OS packages"
  msg_ok "The probe itself is updated from the main server (Probes page)"
  exit
}

# The key is handed to the install script inside the container (the build
# engine passes exported variables along).
if [[ -z "${USS_PROBE_KEY:-}" && -t 0 ]]; then
  echo -e "Paste the enrollment key from the main server (Probes → Add probe),"
  echo -e "or press Enter to enroll later on the probe's web page."
  read -r -p "Enrollment key: " USS_PROBE_KEY
fi
export USS_PROBE_KEY="${USS_PROBE_KEY:-}"

start
build_container
description

# Enroll from the host as well, in case the key did not reach the installer.
if [[ -n "$USS_PROBE_KEY" ]] && ! pct exec "$CTID" -- /usr/local/bin/unify-syslog-probe status 2>/dev/null | grep -q '^State: *connected'; then
  pct exec "$CTID" -- /usr/local/bin/unify-syslog-probe enroll "$USS_PROBE_KEY" || msg_error "Enrollment failed; enroll on the probe page instead"
fi

msg_ok "Completed successfully!\n"
echo -e "${CREATING}${GN}${APP} setup has been successfully initialized!${CL}"
echo -e "${INFO}${YW} Send UniFi syslog to:${CL} ${BGN}${IP}:514 (UDP/TCP)${CL}"
echo -e "${INFO}${YW} Probe page (status, enrollment):${CL}"
echo -e "${TAB}${GATEWAY}${BGN}http://${IP}:8080${CL}"
