# REST API

Everything the web interface does goes through a JSON API under `/api`. Scripts and integrations use the same API with a personal **API token**.

## Tokens

Create a token under **My account → API tokens**. The token (`uss_…`) is shown once; only a hash is stored.

- A token has the **role and sites of its user**. A viewer's token sees only the viewer's sites and cannot export.
- **Read-only** tokens (recommended) can call every `GET` endpoint and the search endpoints (`POST /api/search…`), but nothing that changes data.
- Tokens can expire after 30, 90 or 365 days, or never. Revoke a token by deleting it; admins can see and revoke every user's tokens.
- Tokens cannot manage tokens, passwords or two-factor authentication.
- Every change and export made with a token is in the audit log, marked with the token name.

Send the token in the `Authorization` header:

```sh
TOKEN=uss_0123456789abcdef...
curl -H "Authorization: Bearer $TOKEN" http://syslog.example.com:8080/api/sites
```

Requests with a token do not need the `X-USS` header that the web interface sends.

## Searching

The query language is the same as in the interface (see the README). Times are RFC 3339; without `from`/`to` the last 24 hours are used. `sites` is optional and is always limited to the sites the token may see.

```sh
# Latest 100 blocked packets from one site
curl -s -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"query":"category:firewall action:block","sites":["amsterdam"],"limit":100}' \
  http://syslog.example.com:8080/api/search
```

Response: `{"rows":[{"ts":"…","site":"…","hostname":"…","category":"firewall","action":"block","src_ip":"…",…}],"took_ms":12}`. For the next page, send the `ts` of the last row as `before`.

| Method and path | Body or parameters | Returns |
|---|---|---|
| `POST /api/search` | `query`, `from`, `to`, `sites`, `limit` (max 5000), `before`, `asc` | Log rows |
| `POST /api/search/histogram` | as search, plus `group_by` (a field) | Counts over time |
| `POST /api/search/top` | as search, plus `field` and `n` | Most frequent values of a field |
| `POST /api/search/panels` | `from`, `to`, `sites`, `panels: [{key, query, field, n}]` | Many counts or top lists in one call |
| `GET /api/search/export` | `query`, `from`, `to`, `sites` (comma-separated), `format=csv\|json` | Up to 100,000 rows as CSV or NDJSON (analyst or admin) |
| `GET /api/live` | `query`, `sites`, `after` (unix ms cursor) | Rows received after the cursor, plus a new cursor |

Export example:

```sh
curl -s -H "Authorization: Bearer $TOKEN" -o blocked.csv \
  "http://syslog.example.com:8080/api/search/export?format=csv&query=action:block&from=2026-10-01T00:00:00Z&to=2026-10-02T00:00:00Z"
```

## Inventory and status

| Method and path | Returns |
|---|---|
| `GET /api/me` | The user the token belongs to, with role and sites |
| `GET /api/sites` | Site names |
| `GET /api/sites/overview` | Per site: devices, senders and 24-hour counts |
| `GET /api/devices?days=7` | Device inventory with status (active, silent, inactive) and message counts |
| `GET /api/sources` | Syslog senders (IP address, site, hostname) |
| `GET /api/clients` | Known client names |
| `GET /api/firmware` | Firmware per model and outdated devices |
| `GET /api/categories` | Categories (saved searches) |
| `GET /api/categories/stats` | Message count per category for `from`/`to`/`sites` |
| `GET /api/dashboard` | The overview dashboard data |
| `GET /api/fields` | Field names of the query language |
| `GET /api/stats` | Receiver and ingest counters |
| `GET /api/info` | Version, uptime and storage |
| `GET /api/health` | `{"status":"ok"}` when ClickHouse is reachable (no token needed) |

## Saved searches

| Method and path | Does |
|---|---|
| `GET /api/saved-searches` | Your own saved searches and all shared ones, with `pinned` (in your menu), `mine` and `can_edit` |
| `POST /api/saved-searches` | Save a search: `name`, `description`, `query`, `preset` (`""`, `15m`, `1h`, `6h`, `24h`, `7d`, `30d`, `90d` or `custom` with `from`/`to` as `2006-01-02T15:04`), `site`, `shared`, `pinned` |
| `PUT /api/saved-searches/{id}` | Change a search (owner or administrator) |
| `DELETE /api/saved-searches/{id}` | Delete a search (owner or administrator) |
| `PUT /api/saved-searches/{id}/pin` | Add a search to your menu or remove it: `{"pinned":true}` |

Every user can save private searches; sharing needs the analyst or administrator role.

## Alert rules

| Method and path | Does |
|---|---|
| `GET /api/alerts` | Alert rules |
| `POST /api/alerts`, `PUT /api/alerts/{id}` | Create or change a rule |
| `DELETE /api/alerts/{id}` | Remove a rule |
| `POST /api/alerts/test` | Evaluate the rule in the request body now, without sending: `{"hits":[{"key":"203.0.113.9","value":31}],"groups":4,"top":{…}}` |
| `GET /api/alerts/fields` | Fields a rule can group by or count distinct values of |

A rule has `query`, `threshold`, `window_minutes`, `cooldown_minutes` and `condition`: `above` (at least `threshold` events), `below` (fewer than `threshold`), `distinct` (at least `threshold` different values of `distinct_field`) or `spike` (`spike_percent` more than the average window of the last `baseline_hours`, and at least `threshold`; it needs a quarter of that history). `group_by` evaluates per value of a field. `schedule` limits when the rule runs: `{"days":[1,2,3,4,5],"from":"18:00","to":"08:00"}` (0 = Sunday, server time). These need an administrator.

## System status

| Method and path | Returns |
|---|---|
| `GET /api/system/status` | Health (`ok`, `warning`, `critical`) with the current issues, messages per second (last hour per 10 seconds), dropped and rejected counters, kernel UDP counters, disks, ClickHouse health and storage, process figures and the state of probes, forwarding, notifications and backups |
| `GET /api/settings/retention/usage` | Stored messages and the oldest message per category |

Retention per category is part of the general settings: `PUT /api/settings/general` with `"category_retention": {"wifi": 30, "ips": 365}` next to `retention_days`. These need an administrator.

## Forwarding

| Method and path | Does |
|---|---|
| `GET /api/forward` | Destinations (secrets masked) with live status, queue, 24-hour and total counters, events per hour and the estimated daily volume |
| `POST /api/forward`, `PUT /api/forward/{id}` | Create or change a destination; a masked secret keeps the stored value |
| `DELETE /api/forward/{id}` | Remove a destination and its queue |
| `POST /api/forward/test` | Send one test event with the settings in the request body |
| `POST /api/forward/template` | Install the index template for an Elasticsearch/OpenSearch destination |
| `GET /api/forward/content`, `GET /api/forward/content/{path}` | List and download the bundled Sentinel, Elastic and Logstash content |

These need an administrator.

## Notification channels

| Method and path | Does |
|---|---|
| `GET /api/notify` | Channels (secrets masked) with the last delivery, the last error and the number of waiting notifications |
| `POST /api/notify`, `PUT /api/notify/{id}` | Create or change a channel; a masked secret keeps the stored value |
| `DELETE /api/notify/{id}` | Remove a channel and its waiting notifications |
| `POST /api/notify/test` | Send a test message with the settings in the request body |

Alert rules (`/api/alerts`) take `channels` (channel IDs), `severity` (`info`, `warning`, `critical`) and `email` (default `true`). These need an administrator. See [NOTIFICATIONS.md](NOTIFICATIONS.md).

## Remote probes

| Method and path | Does |
|---|---|
| `GET /api/probes/summary` | Per probe: name, site, status and buffer, limited to the sites of the token's user |
| `GET /api/probes` | All probes with their latest status, plus the server version, the probe address of the server and the probe settings |
| `POST /api/probes` | Create a probe (`name`, `site`, `settings`); returns the enrollment key |
| `PUT /api/probes/{id}` | Change the name, site, or listener and buffer settings |
| `DELETE /api/probes/{id}` | Remove a probe and revoke its key |
| `POST /api/probes/{id}/code` | Create a new enrollment key for a probe |
| `POST /api/probes/update` | Update probes to the server version: `{"ids":[1,2]}` or `{"all":true}` |
| `GET /api/settings/probes`, `PUT /api/settings/probes` | Automatic updates, alert thresholds, recipients and the address probes use |

All of these except the summary need an administrator. The probes themselves use `/api/probe/v1/…` with their own key; see [PROBES.md](PROBES.md).

## Storage, monitoring and high availability

| Method and path | Does |
|---|---|
| `GET /metrics` | Prometheus metrics; a token of an administrator, or a source address allowed under *Metrics & Grafana*. See [MONITORING.md](MONITORING.md) |
| `/clickhouse/` | Read-only ClickHouse HTTP interface for Grafana (basic auth, the password is an administrator's token), when turned on |
| `GET /api/settings/storage` | ClickHouse connection (password masked), stored rows and size, data per disk, storage policies, cold storage settings and a running switch |
| `POST /api/storage/clickhouse/test` | Test a ClickHouse connection: version, existing rows and cluster nodes |
| `PUT /api/settings/storage/clickhouse` | Switch to another ClickHouse: `{"clickhouse":{…},"migrate":true}`, or `{"reset":true}` for the connection in `config.yaml`; the service restarts |
| `PUT /api/settings/storage/cold` | Cold storage settings; see [STORAGE.md](STORAGE.md) |
| `GET /api/settings/monitoring`, `PUT /api/settings/monitoring` | `metrics_enabled`, `anonymous_cidrs` and `grafana_enabled` |
| `GET /api/monitoring/content/{path}` | Download the bundled Grafana dashboards and Prometheus files |
| `GET /api/settings/ha` | Role of this node and the other one, settings copy, pairing code and keepalived configuration |
| `PUT /api/settings/ha` | `{"action":"start"|"join"|"update"|"leave"|"sync", …}`; see [HA.md](HA.md) |

These need an administrator. On a standby node, changes other than storage, high availability and updates are refused with `409`.

Admins can also use the configuration endpoints the interface uses (users, settings, backups and so on) with a read-and-write token. These are not a stable interface and may change between versions.

## Errors

Errors are JSON: `{"error":"…"}` with HTTP status `400` (invalid request), `401` (missing, invalid or expired token), `403` (not allowed for this role or a read-only token) or `404`.
