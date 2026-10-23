// Package integrations bundles ready-made content for the log tools the
// forwarding destinations deliver to: a Sentinel DCR template, KQL queries,
// a workbook and analytics rules; an Elasticsearch index template, Kibana
// saved objects and detection rules; and a Logstash pipeline. For
// monitoring it has Grafana dashboards and data source provisioning, and
// Prometheus alert rules. The web interface offers them for download.
package integrations

import "embed"

//go:embed sentinel elastic logstash grafana prometheus
var FS embed.FS
