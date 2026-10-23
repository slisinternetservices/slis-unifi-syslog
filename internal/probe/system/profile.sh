# UniFi Syslog probe: ask for the enrollment key at the first sign-in.
if [ -t 0 ] && command -v unify-syslog-probe >/dev/null 2>&1; then
  if unify-syslog-probe status 2>/dev/null | grep -q '^State: *not enrolled'; then
    echo
    echo "This UniFi Syslog probe is not connected to a main server yet."
    if [ "$(id -u)" = 0 ]; then
      echo "Paste the enrollment key from the main server (Probes > Add probe), or press Ctrl+C to skip."
      unify-syslog-probe setup || true
    else
      echo "Run: sudo unify-syslog-probe setup   (or open http://$(hostname -I 2>/dev/null | awk '{print $1}'):8080)"
    fi
    echo
  fi
fi
