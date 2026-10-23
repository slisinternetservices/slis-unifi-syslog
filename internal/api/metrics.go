package api

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rhisijmuiden/unify-syslog-server/integrations"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/version"
)

// promWriter writes the Prometheus text exposition format.
type promWriter struct {
	b    bytes.Buffer
	seen map[string]bool
}

func (p *promWriter) metric(name, typ, help string, value float64, labels ...string) {
	if p.seen == nil {
		p.seen = map[string]bool{}
	}
	if !p.seen[name] {
		p.seen[name] = true
		fmt.Fprintf(&p.b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	p.b.WriteString(name)
	if len(labels) > 0 {
		p.b.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				p.b.WriteByte(',')
			}
			fmt.Fprintf(&p.b, "%s=%s", labels[i], strconv.Quote(labels[i+1]))
		}
		p.b.WriteByte('}')
	}
	p.b.WriteByte(' ')
	p.b.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
	p.b.WriteByte('\n')
}

func (p *promWriter) counter(name, help string, v float64, labels ...string) {
	p.metric(name, "counter", help, v, labels...)
}
func (p *promWriter) gauge(name, help string, v float64, labels ...string) {
	p.metric(name, "gauge", help, v, labels...)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// metricsAuth lets Prometheus in with an API token (any role) or from an
// allowed network without one.
func (s *Server) metricsAuth(r *http.Request) bool {
	if tok := bearer(r); tok != "" {
		// Metrics name destinations, probes and sites of every customer, so
		// only administrators read them.
		t, u, err := s.Meta.TokenUser(tok)
		if err != nil || u.Role != store.RoleAdmin {
			return false
		}
		s.Meta.TouchToken(t, clientIP(r))
		return true
	}
	return store.InCIDRs(peerIP(r), s.Meta.Monitoring().AnonymousCIDRs)
}

// metrics serves /metrics for Prometheus.
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	if !s.Meta.Monitoring().MetricsEnabled {
		http.NotFound(w, r)
		return
	}
	if !s.metricsAuth(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="unify-syslog"`)
		jerr(w, 401, "an API token is required (Authorization: Bearer uss_…)")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var p promWriter
	p.gauge("uss_build_info", "Version of the server.", 1, "version", version.Version, "commit", version.Commit)
	p.gauge("uss_start_time_seconds", "Start time of the process.", float64(s.Started.Unix()))

	st := s.Status.Status(ctx)
	in := st.Ingest
	p.counter("uss_syslog_received_total", "Syslog messages received (UDP packets and TCP messages).", float64(in.Received))
	p.counter("uss_syslog_received_by_protocol_total", "Syslog messages received per protocol.", float64(in.UDPPackets), "protocol", "udp")
	p.counter("uss_syslog_received_by_protocol_total", "Syslog messages received per protocol.", float64(in.TCPMessages), "protocol", "tcp")
	p.gauge("uss_syslog_tcp_connections", "Open syslog TCP connections.", float64(in.TCPConns))
	p.counter("uss_syslog_rejected_total", "Messages from sources not on the allowlist.", float64(in.Rejected))
	p.counter("uss_ingest_dropped_total", "Messages dropped because the ingest queue was full.", float64(in.Dropped))
	if v, ok := in.Kernel["RcvbufErrors"]; ok {
		p.counter("uss_kernel_udp_receive_buffer_errors_total", "UDP packets the kernel dropped (receive buffer full), for the whole host.", float64(v))
	}
	p.counter("uss_ingest_parsed_total", "Messages parsed, probes included.", float64(in.Parsed))
	p.counter("uss_ingest_stored_total", "Messages stored in ClickHouse.", float64(in.Inserted))
	p.counter("uss_ingest_insert_errors_total", "Failed ClickHouse inserts.", float64(in.InsertErrs))
	p.counter("uss_ingest_discarded_total", "Messages discarded after inserts kept failing.", float64(in.Discarded))
	p.gauge("uss_ingest_queue_length", "Messages waiting in the ingest queue.", float64(in.QueueLen))
	p.gauge("uss_ingest_queue_capacity", "Size of the ingest queue.", float64(in.QueueCap))
	p.gauge("uss_ingest_pending", "Messages parsed but not stored yet.", float64(in.Pending))
	if in.LastInsert > 0 {
		p.gauge("uss_ingest_last_insert_timestamp_seconds", "Time of the last successful insert.", float64(in.LastInsert)/1000)
	}

	h := st.Store
	p.gauge("uss_clickhouse_up", "Whether ClickHouse answers.", b2f(h.OK))
	p.gauge("uss_clickhouse_latency_seconds", "ClickHouse ping time.", h.LatencyMS/1000)
	p.gauge("uss_clickhouse_parts", "Active data parts of the logs table.", float64(h.Parts))
	p.gauge("uss_clickhouse_max_parts_per_partition", "Most active parts in one partition.", float64(h.MaxPartsInDay))
	p.gauge("uss_clickhouse_merges", "Running merges.", float64(h.Merges))
	p.gauge("uss_clickhouse_mutations", "Unfinished mutations.", float64(h.Mutations))
	p.gauge("uss_clickhouse_memory_bytes", "Memory used by ClickHouse.", float64(h.MemoryBytes))
	for _, d := range st.Disks {
		p.gauge("uss_disk_total_bytes", "Size of a disk used by the server.", float64(d.Total), "disk", d.Name, "path", d.Path)
		p.gauge("uss_disk_free_bytes", "Free space of a disk used by the server.", float64(d.Free), "disk", d.Name, "path", d.Path)
	}
	if disks, err := s.Logs.TableDisks(ctx); err == nil {
		for _, d := range disks {
			p.gauge("uss_logs_rows", "Stored log rows per storage disk.", float64(d.Rows), "disk", d.Disk, "type", d.Type)
			p.gauge("uss_logs_bytes", "Compressed size of the stored logs per storage disk.", float64(d.Bytes), "disk", d.Disk, "type", d.Type)
			if !d.Oldest.IsZero() {
				p.gauge("uss_logs_oldest_timestamp_seconds", "Oldest log row per storage disk.", float64(d.Oldest.Unix()), "disk", d.Disk)
			}
		}
	}
	issues := map[string]int{}
	for _, is := range st.Issues {
		issues[is.Severity]++
	}
	for _, sev := range []string{store.SevWarning, store.SevCritical} {
		p.gauge("uss_status_issues", "Open problems on the System status page.", float64(issues[sev]), "severity", sev)
	}

	p.gauge("uss_process_resident_memory_bytes", "Resident memory of the server process.", float64(st.Process.RSSBytes))
	p.gauge("uss_process_heap_bytes", "Go heap in use.", float64(st.Process.HeapBytes))
	p.gauge("uss_process_goroutines", "Goroutines.", float64(st.Process.Goroutines))

	if dests, err := s.Meta.ForwardDestinations(); err == nil {
		stats := s.Forward.Statuses()
		totals, _ := s.Meta.ForwardTotals()
		for _, d := range dests {
			fs := stats[d.ID]
			l := []string{"destination", d.Name, "kind", d.Kind}
			p.gauge("uss_forward_up", "Forwarding destination delivers without errors (1), fails (0).", b2f(fs.State != "error"), l...)
			p.gauge("uss_forward_queue_events", "Events waiting in the forwarding queue.", float64(fs.QueueRecs), l...)
			p.gauge("uss_forward_queue_bytes", "Size of the forwarding queue on disk.", float64(fs.QueueSize), l...)
			t := totals[d.ID]
			p.counter("uss_forward_events_total", "Events delivered to the destination.", float64(t.Events), l...)
			p.counter("uss_forward_bytes_total", "Bytes delivered to the destination.", float64(t.Bytes), l...)
			p.counter("uss_forward_errors_total", "Failed deliveries.", float64(t.Errors), l...)
			p.counter("uss_forward_rejected_total", "Events the destination refused.", float64(t.Rejected), l...)
			p.counter("uss_forward_dropped_total", "Events dropped because the queue was full.", float64(t.Dropped), l...)
		}
	}

	if probes, err := s.Meta.Probes(); err == nil {
		online := 0
		for _, pr := range probes {
			up := s.Probes.Online(pr)
			if up {
				online++
			}
			l := []string{"probe", pr.Name, "site", pr.Site}
			p.gauge("uss_probe_up", "Probe has reported in recently.", b2f(up), l...)
			p.gauge("uss_probe_buffer_bytes", "Size of the probe's disk buffer.", float64(pr.Status.BufferBytes), l...)
			p.gauge("uss_probe_last_seen_timestamp_seconds", "Last contact with the probe.", float64(pr.LastSeen), l...)
		}
		p.gauge("uss_probes", "Probes by state.", float64(online), "state", "online")
		p.gauge("uss_probes", "Probes by state.", float64(len(probes)-online), "state", "offline")
	}

	if recs, err := s.Meta.BackupRecords(200); err == nil {
		var lastOK, lastFull int64
		failed := 0.0
		for i, rec := range recs {
			if rec.Status == "ok" {
				lastOK = max(lastOK, rec.CreatedAt)
				if rec.Kind == "full" {
					lastFull = max(lastFull, rec.CreatedAt)
				}
			}
			if i == 0 && rec.Status == "failed" {
				failed = 1
			}
		}
		p.gauge("uss_backup_last_success_timestamp_seconds", "Last successful backup.", float64(lastOK))
		p.gauge("uss_backup_last_full_timestamp_seconds", "Last successful full backup.", float64(lastFull))
		p.gauge("uss_backup_last_failed", "The most recent backup failed.", failed)
	}
	p.gauge("uss_backup_running", "A backup or restore is running.", b2f(s.Backup.Status().Running))

	if chans, err := s.Meta.NotifyChannels(); err == nil {
		q := s.Meta.NotifyQueueCounts()
		for _, c := range chans {
			p.gauge("uss_notify_queue", "Notifications waiting per channel (quiet hours, batching, retries).", float64(q[c.ID]), "channel", c.Name, "kind", c.Kind)
		}
	}

	if s.HA != nil {
		hs := s.HA.State()
		p.gauge("uss_ha_enabled", "High availability is set up on this node.", b2f(hs.Enabled))
		p.gauge("uss_ha_active", "This node holds the virtual IP and runs the scheduled jobs.", b2f(hs.Active))
		if hs.LastSync > 0 {
			p.gauge("uss_ha_last_sync_timestamp_seconds", "Last time this node took over settings from the active node.", float64(hs.LastSync))
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write(p.b.Bytes())
}

func (s *Server) getMonitoring(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, map[string]any{"settings": s.Meta.Monitoring(), "base_url": s.baseURL(r)})
}

func (s *Server) putMonitoring(w http.ResponseWriter, r *http.Request) {
	var m store.MonitoringSettings
	if err := decode(r, &m); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	cidrs, err := store.CleanCIDRs(m.AnonymousCIDRs)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	m.AnonymousCIDRs = cidrs
	if err := s.Meta.SetSetting("monitoring", m); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "settings_monitoring", "")
	jsonOut(w, map[string]any{"settings": m, "base_url": s.baseURL(r)})
}

// monitoringContentFile downloads the bundled Grafana dashboards and
// Prometheus files.
func (s *Server) monitoringContentFile(w http.ResponseWriter, r *http.Request) {
	p := path.Clean(chi.URLParam(r, "*"))
	if !strings.HasPrefix(p, "grafana/") && !strings.HasPrefix(p, "prometheus/") {
		jerr(w, 404, "not found")
		return
	}
	b, err := fs.ReadFile(integrations.FS, p)
	if err != nil {
		jerr(w, 404, "not found")
		return
	}
	ct := "application/json"
	if !strings.HasSuffix(p, ".json") {
		ct = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="`+path.Base(p)+`"`)
	w.Write(b)
}
