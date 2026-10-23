# Logstash

`unifi-syslog.conf` is a sample pipeline that receives events from UniFi Syslog Server and writes them to the Elasticsearch data stream `logs-unifi.syslog-default`.

- **TCP** (port 5514): one ECS JSON document per line, optionally over TLS. Configure the destination in the app as *Logstash*, transport TCP or TLS.
- **HTTP** (port 8085): a JSON array of ECS documents per request. Configure transport HTTP, URL `http://logstash:8085/`.

Steps:

1. Install the index template from [`../elastic/index-template.json`](../elastic/index-template.json) in Elasticsearch (see the Elastic README).
2. Copy `unifi-syslog.conf` to `/etc/logstash/conf.d/`, set `hosts` and the `ES_API_KEY` environment variable (or keystore entry), and uncomment the TLS settings if you use TLS.
3. Restart Logstash and press **Test** on the destination in the app.

The `filter` block has commented examples for enrichment (CIDR tagging, site-to-organization lookup) and for dropping categories.
