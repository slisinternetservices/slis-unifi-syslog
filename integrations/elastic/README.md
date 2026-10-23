# Elasticsearch, Elastic Cloud and OpenSearch

UniFi Syslog Server writes events with the Bulk API into the data stream `logs-unifi.syslog-default` (configurable), as Elastic Common Schema (ECS) documents. Retried batches reuse the same `_id`, so retries do not create duplicates.

| File | What it is |
|---|---|
| `index-template.json` | Composable index template for `logs-unifi.syslog-*` with ECS mappings |
| `kibana-saved-objects.ndjson` | Data view, saved searches, visualizations and the "UniFi Syslog overview" dashboard |
| `detection-rules.ndjson` | Five Elastic Security rules (disabled) |

## 1. Create an API key

The app needs to write documents and, for the first write, create the data stream. In **Kibana → Stack Management → API keys → Create API key**, or with the API:

```json
POST /_security/api_key
{
  "name": "unify-syslog-forwarder",
  "role_descriptors": {
    "unifi_writer": {
      "indices": [
        { "names": ["logs-unifi.syslog-*"], "privileges": ["create_doc", "auto_configure"] }
      ]
    }
  }
}
```

Use the `encoded` value as the API key in the app. To let the app install the index template itself, the key also needs the cluster privilege `manage_index_templates`; otherwise install it once as an admin (step 2).

**Elastic Cloud**: use the Elasticsearch endpoint (`https://<deployment>.es.<region>.<provider>.elastic-cloud.com`) or the Cloud ID.

**OpenSearch**: API keys do not exist; use basic auth with a user whose role has `create_index`, `write` (or `index`) and `indices:admin/mapping/put` on `logs-unifi.syslog-*`. Data streams need OpenSearch 2.0 or later.

## 2. Install the index template

In the app: **Forwarding → (destination) → Install index template**. It uses the data stream name configured on the destination.

By hand (Kibana Dev Tools or curl):

```sh
curl -u elastic -H 'Content-Type: application/json' \
  -X PUT https://es.example.com:9200/_index_template/logs-unifi.syslog \
  -d @index-template.json
```

Install the template before the first event arrives. If the data stream already exists, roll it over (`POST logs-unifi.syslog-default/_rollover`) so the new mappings apply. The template sets no ILM policy; attach one in **Index Lifecycle Policies** (or an ISM policy in OpenSearch) to control retention.

If you use another data stream name, change `index_patterns` (the app does this when it installs the template).

## 3. Import the Kibana saved objects

**Stack Management → Saved objects → Import**, choose `kibana-saved-objects.ndjson`. Works with Kibana 7.10 and later; Kibana 8 migrates the objects on import. You get:

- Data view `logs-unifi.syslog-*`
- Saved searches: firewall blocks, IPS alerts, sign-in failures, Wi-Fi events
- Visualizations: events over time by category, top blocked source IPs, destination countries of blocked traffic, top blocked ports, IPS signatures, sign-in failures by source IP
- Dashboard **UniFi Syslog overview**

OpenSearch Dashboards can import the same file under **Dashboards Management → Saved objects**; the data view is called an index pattern there.

## 4. Import the detection rules

**Security → Rules → Import rules**, choose `detection-rules.ndjson`. The rules are imported **disabled**:

| Rule | Type |
|---|---|
| SSH or admin sign-in brute force | Threshold: 10 failures per `source.ip` |
| High severity IPS alert | Query: IPS with syslog severity 2 or lower |
| Port scan from one source | Threshold with cardinality: 25+ distinct `destination.port` per `source.ip` |
| VPN authentication failures | Threshold: 5 failures per `source.ip` |
| UniFi Access door forced open | Query |

OpenSearch uses Alerting / Security Analytics instead; use the queries in the rules as a starting point.
