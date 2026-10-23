package store

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/rhisijmuiden/unify-syslog-server/internal/config"
	"github.com/rhisijmuiden/unify-syslog-server/internal/query"
	"github.com/rhisijmuiden/unify-syslog-server/internal/unifi"
)

type Logs struct {
	Conn    driver.Conn
	cfg     config.ClickHouse
	db      string
	cluster string
	http    *http.Client

	mu sync.Mutex
	// retention is the last applied retention; move the cold storage TTL
	// rule. Changing one rewrites the TTL with both.
	retention retention
	move      string
}

type retention struct {
	days        int
	perCategory map[string]int
}

var clusterRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// TLSConfig is the client TLS setup for a ClickHouse connection, or nil
// without TLS.
func TLSConfig(cfg config.ClickHouse, host string) (*tls.Config, error) {
	if !cfg.Secure && !strings.HasPrefix(cfg.HTTPURL, "https:") {
		return nil, nil
	}
	t := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.SkipVerify, ServerName: host}
	if cfg.CACert != "" {
		pem, err := os.ReadFile(cfg.CACert)
		if err != nil {
			return nil, fmt.Errorf("ca certificate: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca certificate: no PEM certificates in %s", cfg.CACert)
		}
		t.RootCAs = pool
	}
	return t, nil
}

// Connect opens a connection to ClickHouse without touching the schema.
func Connect(ctx context.Context, cfg config.ClickHouse) (driver.Conn, error) {
	if cfg.Cluster != "" && !clusterRe.MatchString(cfg.Cluster) {
		return nil, fmt.Errorf("invalid cluster name %q", cfg.Cluster)
	}
	addrs := cfg.Addresses()
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no ClickHouse address configured")
	}
	opts := &clickhouse.Options{
		Addr:             addrs,
		Auth:             clickhouse.Auth{Database: "default", Username: cfg.Username, Password: cfg.Password},
		Compression:      &clickhouse.Compression{Method: clickhouse.CompressionLZ4},
		DialTimeout:      10 * time.Second,
		MaxOpenConns:     16,
		ConnMaxLifetime:  time.Hour,
		ConnOpenStrategy: clickhouse.ConnOpenInOrder,
		Settings:         clickhouse.Settings{"max_execution_time": 120},
	}
	if cfg.Secure {
		host, _, _ := net.SplitHostPort(addrs[0])
		t, err := TLSConfig(cfg, host)
		if err != nil {
			return nil, err
		}
		opts.TLS = t
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, err
	}
	if err := conn.Ping(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("clickhouse ping: %w", err)
	}
	return conn, nil
}

func httpClient(cfg config.ClickHouse) (*http.Client, error) {
	u, err := url.Parse(cfg.HTTPURL)
	if err != nil {
		return nil, fmt.Errorf("http url: %w", err)
	}
	t, err := TLSConfig(cfg, u.Hostname())
	if err != nil {
		return nil, err
	}
	if t == nil {
		return http.DefaultClient, nil
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = t
	return &http.Client{Transport: tr}, nil
}

func OpenLogs(ctx context.Context, cfg config.ClickHouse, retentionDays int) (*Logs, error) {
	conn, err := Connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	hc, err := httpClient(cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	l := &Logs{Conn: conn, cfg: cfg, db: cfg.Database, cluster: cfg.Cluster, http: hc}
	if err := l.migrate(ctx, retentionDays); err != nil {
		conn.Close()
		return nil, err
	}
	return l, nil
}

func (l *Logs) t(name string) string { return "`" + l.db + "`." + name }

// Cluster is the ClickHouse cluster the logs are stored on ("" for one server).
func (l *Logs) Cluster() string { return l.cluster }

// HTTPClient is the client for the ClickHouse HTTP interface (with the TLS
// settings of the connection).
func (l *Logs) HTTPClient() *http.Client { return l.http }

// Config is the connection the log store was opened with.
func (l *Logs) Config() config.ClickHouse { return l.cfg }

// onCluster is the ON CLUSTER clause for DDL in cluster mode.
func (l *Logs) onCluster() string {
	if l.cluster == "" {
		return ""
	}
	return " ON CLUSTER `" + l.cluster + "`"
}

// local is the table that holds the data of name: on a cluster the
// replicated table behind the Distributed one; DDL that changes data
// settings (TTL, storage policy, truncate) targets it.
func (l *Logs) local(name string) string {
	if l.cluster == "" {
		return l.t(name)
	}
	return l.t(name + "_local")
}

// localName is local without the database, for system tables.
func (l *Logs) localName(name string) string {
	if l.cluster == "" {
		return name
	}
	return name + "_local"
}

// system reads a system table of every node of the cluster.
func (l *Logs) system(table string) string {
	if l.cluster == "" {
		return "system." + table
	}
	return "clusterAllReplicas('" + l.cluster + "', system." + table + ")"
}

func (l *Logs) migrate(ctx context.Context, retention int) error {
	if l.cluster != "" {
		// A database created without a cluster has a plain logs table; the
		// cluster tables would be created next to it and never read.
		var engine string
		err := l.Conn.QueryRow(ctx, "SELECT engine FROM system.tables WHERE database = ? AND name = 'logs'", l.db).Scan(&engine)
		if err == nil && engine != "Distributed" {
			return fmt.Errorf("database %s already holds logs of a single server (%s); use another database name for the cluster", l.db, engine)
		}
	}
	for _, s := range l.schema(retention) {
		if err := l.Conn.Exec(ctx, s); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

const logColumns = `ts DateTime64(3, 'UTC') CODEC(Delta, ZSTD(1)),
			received DateTime64(3, 'UTC') CODEC(Delta, ZSTD(1)),
			source_ip LowCardinality(String),
			site LowCardinality(String),
			hostname LowCardinality(String),
			device_model LowCardinality(String),
			device_mac LowCardinality(String),
			device_version LowCardinality(String),
			facility UInt8,
			severity UInt8,
			app LowCardinality(String),
			pid String CODEC(ZSTD(1)),
			category LowCardinality(String),
			event_type LowCardinality(String),
			action LowCardinality(String),
			rule LowCardinality(String),
			src_ip String CODEC(ZSTD(1)),
			dst_ip String CODEC(ZSTD(1)),
			src_port UInt16,
			dst_port UInt16,
			proto LowCardinality(String),
			in_if LowCardinality(String),
			out_if LowCardinality(String),
			client_mac String CODEC(ZSTD(1)),
			client_name String CODEC(ZSTD(1)),
			client_ip String CODEC(ZSTD(1)),
			ssid LowCardinality(String),
			message String CODEC(ZSTD(3)),
			raw String CODEC(ZSTD(3)),
			fields Map(LowCardinality(String), String) CODEC(ZSTD(3)),
			INDEX idx_msg lower(message) TYPE tokenbf_v1(32768, 3, 0) GRANULARITY 4,
			INDEX idx_src src_ip TYPE bloom_filter(0.01) GRANULARITY 4,
			INDEX idx_dst dst_ip TYPE bloom_filter(0.01) GRANULARITY 4,
			INDEX idx_cmac client_mac TYPE bloom_filter(0.01) GRANULARITY 4,
			INDEX idx_cip client_ip TYPE bloom_filter(0.01) GRANULARITY 4,
			INDEX idx_dport dst_port TYPE set(1000) GRANULARITY 4`

const hourlyColumns = `hour DateTime('UTC'),
			site LowCardinality(String),
			category LowCardinality(String),
			action LowCardinality(String),
			severity UInt8,
			source_ip LowCardinality(String),
			cnt UInt64`

// schema lists the statements that create or upgrade the log store. They
// must stay additive (CREATE ... IF NOT EXISTS, ADD COLUMN IF NOT EXISTS)
// so an older version still reads the data after a rollback; see
// TestLogSchemaIsAdditive.
//
// On a cluster every node holds replicated logs_local and
// logs_hourly_local tables, and logs and logs_hourly are Distributed
// tables over them, so every query reads the whole cluster.
func (l *Logs) schema(retention int) []string {
	if retention <= 0 {
		retention = 90
	}
	on := l.onCluster()
	engine, hourlyEngine := "MergeTree", "SummingMergeTree(cnt)"
	if l.cluster != "" {
		engine, hourlyEngine = "ReplicatedMergeTree", "ReplicatedSummingMergeTree(cnt)"
	}
	out := []string{
		"CREATE DATABASE IF NOT EXISTS `" + l.db + "`" + on,
		`CREATE TABLE IF NOT EXISTS ` + l.local("logs") + on + ` (
			` + logColumns + `
		) ENGINE = ` + engine + `
		PARTITION BY toYYYYMMDD(ts)
		ORDER BY (site, category, ts)
		TTL toDateTime(ts) + INTERVAL ` + fmt.Sprint(retention) + ` DAY DELETE
		SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1`,
		`CREATE TABLE IF NOT EXISTS ` + l.local("logs_hourly") + on + ` (
			` + hourlyColumns + `
		) ENGINE = ` + hourlyEngine + `
		PARTITION BY toYYYYMM(hour)
		ORDER BY (hour, site, category, action, severity, source_ip)
		TTL hour + INTERVAL 400 DAY DELETE`,
	}
	if l.cluster != "" {
		out = append(out,
			`CREATE TABLE IF NOT EXISTS `+l.t("logs")+on+` AS `+l.local("logs")+
				` ENGINE = Distributed('`+l.cluster+`', '`+l.db+`', 'logs_local', rand())`,
			`CREATE TABLE IF NOT EXISTS `+l.t("logs_hourly")+on+` AS `+l.local("logs_hourly")+
				` ENGINE = Distributed('`+l.cluster+`', '`+l.db+`', 'logs_hourly_local', rand())`)
	}
	return append(out,
		`CREATE MATERIALIZED VIEW IF NOT EXISTS `+l.t("logs_hourly_mv")+on+` TO `+l.local("logs_hourly")+` AS
			SELECT toStartOfHour(ts) AS hour, site, category, action, severity, source_ip, count() AS cnt
			FROM `+l.local("logs")+` GROUP BY hour, site, category, action, severity, source_ip`)
}

func (l *Logs) Insert(ctx context.Context, evs []*unifi.Event) error {
	// Naming the columns keeps inserts working when a later version adds
	// columns to the table and this one is rolled back to.
	b, err := l.Conn.PrepareBatch(ctx, "INSERT INTO "+l.t("logs")+" ("+insertCols+")")
	if err != nil {
		return err
	}
	for _, e := range evs {
		f := e.Fields
		if f == nil {
			f = map[string]string{}
		}
		if err := b.Append(e.TS, e.Received, e.SourceIP, e.Site, e.Hostname, e.DeviceModel, e.DeviceMAC, e.DeviceVersion,
			e.Facility, e.Severity, e.App, e.PID, e.Category, e.EventType, e.Action, e.Rule,
			e.SrcIP, e.DstIP, e.SrcPort, e.DstPort, e.Proto, e.InIf, e.OutIf,
			e.ClientMAC, e.ClientName, e.ClientIP, e.SSID, e.Message, e.Raw, f); err != nil {
			b.Abort()
			return err
		}
	}
	return b.Send()
}

// ---------------------------------------------------------------- search

type SearchReq struct {
	Query  string    `json:"query"`
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Sites  []string  `json:"sites"`
	Limit  int       `json:"limit"`
	Before time.Time `json:"before"` // keyset pagination: rows strictly older
	// AfterReceived limits to rows received after this instant (live tail).
	AfterReceived time.Time `json:"-"`
	Asc           bool      `json:"asc"`
}

type Row struct {
	TS            time.Time         `json:"ts"`
	Received      time.Time         `json:"received"`
	SourceIP      string            `json:"source_ip"`
	Site          string            `json:"site"`
	Hostname      string            `json:"hostname"`
	DeviceModel   string            `json:"device_model"`
	DeviceMAC     string            `json:"device_mac"`
	DeviceVersion string            `json:"device_version"`
	Facility      uint8             `json:"facility"`
	Severity      uint8             `json:"severity"`
	App           string            `json:"app"`
	Category      string            `json:"category"`
	EventType     string            `json:"event_type"`
	Action        string            `json:"action"`
	Rule          string            `json:"rule"`
	SrcIP         string            `json:"src_ip"`
	DstIP         string            `json:"dst_ip"`
	SrcPort       uint16            `json:"src_port"`
	DstPort       uint16            `json:"dst_port"`
	Proto         string            `json:"proto"`
	InIf          string            `json:"in_if"`
	OutIf         string            `json:"out_if"`
	ClientMAC     string            `json:"client_mac"`
	ClientName    string            `json:"client_name"`
	ClientIP      string            `json:"client_ip"`
	SSID          string            `json:"ssid"`
	Message       string            `json:"message"`
	Raw           string            `json:"raw"`
	Fields        map[string]string `json:"fields"`
}

func RowFromEvent(e *unifi.Event) Row {
	return Row{TS: e.TS, Received: e.Received, SourceIP: e.SourceIP, Site: e.Site, Hostname: e.Hostname, DeviceModel: e.DeviceModel,
		DeviceMAC: e.DeviceMAC, DeviceVersion: e.DeviceVersion, Facility: e.Facility, Severity: e.Severity, App: e.App,
		Category: e.Category, EventType: e.EventType, Action: e.Action, Rule: e.Rule, SrcIP: e.SrcIP, DstIP: e.DstIP,
		SrcPort: e.SrcPort, DstPort: e.DstPort, Proto: e.Proto, InIf: e.InIf, OutIf: e.OutIf, ClientMAC: e.ClientMAC,
		ClientName: e.ClientName, ClientIP: e.ClientIP, SSID: e.SSID, Message: e.Message, Raw: e.Raw, Fields: e.Fields}
}

const insertCols = `ts, received, source_ip, site, hostname, device_model, device_mac, device_version, facility, severity, app, pid,
	category, event_type, action, rule, src_ip, dst_ip, src_port, dst_port, proto, in_if, out_if,
	client_mac, client_name, client_ip, ssid, message, raw, fields`

const rowCols = `ts, received, source_ip, site, hostname, device_model, device_mac, device_version, facility, severity, app,
	category, event_type, action, rule, src_ip, dst_ip, src_port, dst_port, proto, in_if, out_if,
	client_mac, client_name, client_ip, ssid, message, raw, fields`

func (l *Logs) where(r SearchReq) (string, []any, error) {
	f, err := query.Compile(r.Query)
	if err != nil {
		return "", nil, err
	}
	w := []string{"ts >= ?", "ts <= ?", f.Where}
	args := []any{r.From, r.To}
	args = append(args, f.Args...)
	if len(r.Sites) > 0 {
		w = append(w, "site IN ?")
		args = append(args, r.Sites)
	}
	if !r.AfterReceived.IsZero() {
		w = append(w, "received > ?")
		args = append(args, r.AfterReceived)
	}
	return strings.Join(w, " AND "), args, nil
}

func (l *Logs) Search(ctx context.Context, r SearchReq) ([]Row, error) {
	if r.Limit <= 0 || r.Limit > 5000 {
		r.Limit = 200
	}
	w, args, err := l.where(r)
	if err != nil {
		return nil, err
	}
	if !r.Before.IsZero() {
		w += " AND ts < ?"
		args = append(args, r.Before)
	}
	order := "DESC"
	if r.Asc {
		order = "ASC"
	}
	rows, err := l.Conn.Query(ctx, fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY ts %s LIMIT %d", rowCols, l.t("logs"), w, order, r.Limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		var x Row
		if err := rows.Scan(&x.TS, &x.Received, &x.SourceIP, &x.Site, &x.Hostname, &x.DeviceModel, &x.DeviceMAC, &x.DeviceVersion,
			&x.Facility, &x.Severity, &x.App, &x.Category, &x.EventType, &x.Action, &x.Rule, &x.SrcIP, &x.DstIP,
			&x.SrcPort, &x.DstPort, &x.Proto, &x.InIf, &x.OutIf, &x.ClientMAC, &x.ClientName, &x.ClientIP, &x.SSID,
			&x.Message, &x.Raw, &x.Fields); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

type Bucket struct {
	T     time.Time `json:"t"`
	Key   string    `json:"key"`
	Count uint64    `json:"count"`
}

// BucketSeconds picks a histogram resolution giving ~120 buckets.
func BucketSeconds(from, to time.Time) int {
	span := to.Sub(from).Seconds()
	for _, s := range []int{10, 30, 60, 300, 600, 1800, 3600, 3 * 3600, 6 * 3600, 12 * 3600, 86400} {
		if span/float64(s) <= 150 {
			return s
		}
	}
	return 86400
}

// Histogram returns counts per time bucket, split by groupBy (a column name
// from a fixed allow-list) when given.
func (l *Logs) Histogram(ctx context.Context, r SearchReq, groupBy string) ([]Bucket, int, error) {
	w, args, err := l.where(r)
	if err != nil {
		return nil, 0, err
	}
	g := "''"
	switch groupBy {
	case "category", "severity", "site", "action", "hostname", "app", "event_type":
		g = "toString(" + groupBy + ")"
	}
	step := BucketSeconds(r.From, r.To)
	q := fmt.Sprintf("SELECT toStartOfInterval(ts, INTERVAL %d SECOND) AS b, %s AS k, count() FROM %s WHERE %s GROUP BY b, k ORDER BY b", step, g, l.t("logs"), w)
	rows, err := l.Conn.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Bucket{}
	for rows.Next() {
		var b Bucket
		var t time.Time
		if err := rows.Scan(&t, &b.Key, &b.Count); err != nil {
			return nil, 0, err
		}
		b.T = t
		out = append(out, b)
	}
	return out, step, rows.Err()
}

func (l *Logs) Count(ctx context.Context, r SearchReq) (uint64, error) {
	w, args, err := l.where(r)
	if err != nil {
		return 0, err
	}
	var n uint64
	err = l.Conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s WHERE %s", l.t("logs"), w), args...).Scan(&n)
	return n, err
}

type TopItem struct {
	Key   string `json:"key"`
	Label string `json:"label,omitempty"`
	Count uint64 `json:"count"`
}

var topCols = map[string]string{
	"src_ip": "src_ip", "dst_ip": "dst_ip", "dst_port": "toString(dst_port)", "rule": "rule", "client_mac": "client_mac",
	"client_name": "client_name", "ssid": "ssid", "hostname": "hostname", "source_ip": "source_ip", "app": "app",
	"event_type": "event_type", "category": "category", "site": "site", "proto": "proto", "action": "action",
	"severity": "toString(severity)", "device_model": "device_model", "in_if": "in_if", "out_if": "out_if",
	"src_country": "fields['src_country']", "dst_country": "fields['dst_country']",
	"src_as": "concat(fields['src_asn'], '|', fields['src_as_org'])", "dst_as": "concat(fields['dst_asn'], '|', fields['dst_as_org'])",
	"device_name": "fields['device_name']", "classification": "fields['classification']", "client_ip": "client_ip",
	"src_name": "fields['src_name']", "dst_name": "fields['dst_name']",
	"camera": "fields['camera']", "door": "fields['door']", "user": "fields['user']",
}

func (l *Logs) Top(ctx context.Context, r SearchReq, field string, n int) ([]TopItem, error) {
	c, ok := topCols[field]
	if !ok {
		return nil, fmt.Errorf("unknown field %q", field)
	}
	w, args, err := l.where(r)
	if err != nil {
		return nil, err
	}
	if n <= 0 || n > 100 {
		n = 10
	}
	q := fmt.Sprintf("SELECT %s AS k, count() AS c FROM %s WHERE %s AND k != '' AND k != '0' AND k != '|' GROUP BY k ORDER BY c DESC LIMIT %d", c, l.t("logs"), w, n)
	rows, err := l.Conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TopItem{}
	for rows.Next() {
		var t TopItem
		if err := rows.Scan(&t.Key, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- dashboard (pre-aggregated)

type Overview struct {
	Total      uint64            `json:"total"`
	Blocked    uint64            `json:"blocked"`
	Threats    uint64            `json:"threats"`
	Errors     uint64            `json:"errors"`
	ByCategory []TopItem         `json:"by_category"`
	BySite     []TopItem         `json:"by_site"`
	BySeverity []TopItem         `json:"by_severity"`
	Timeline   []Bucket          `json:"timeline"`
	StepSec    int               `json:"step_sec"`
	Extra      map[string]uint64 `json:"extra,omitempty"`
}

func (l *Logs) Overview(ctx context.Context, from, to time.Time, sites []string) (*Overview, error) {
	w := "hour >= toStartOfHour(?) AND hour <= ?"
	args := []any{from, to}
	if len(sites) > 0 {
		w += " AND site IN ?"
		args = append(args, sites)
	}
	o := &Overview{}
	if err := l.Conn.QueryRow(ctx, fmt.Sprintf(`SELECT sum(cnt), sumIf(cnt, action IN ('block','reject')), sumIf(cnt, category='ips'), sumIf(cnt, severity<=3) FROM %s WHERE %s`, l.t("logs_hourly"), w), args...).
		Scan(&o.Total, &o.Blocked, &o.Threats, &o.Errors); err != nil {
		return nil, err
	}
	agg := func(col string) ([]TopItem, error) {
		rows, err := l.Conn.Query(ctx, fmt.Sprintf("SELECT toString(%s) AS k, sum(cnt) AS c FROM %s WHERE %s GROUP BY k ORDER BY c DESC", col, l.t("logs_hourly"), w), args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []TopItem{}
		for rows.Next() {
			var t TopItem
			if err := rows.Scan(&t.Key, &t.Count); err != nil {
				return nil, err
			}
			out = append(out, t)
		}
		return out, rows.Err()
	}
	var err error
	if o.ByCategory, err = agg("category"); err != nil {
		return nil, err
	}
	if o.BySite, err = agg("site"); err != nil {
		return nil, err
	}
	if o.BySeverity, err = agg("severity"); err != nil {
		return nil, err
	}
	step := 3600
	span := to.Sub(from)
	if span > 8*24*time.Hour {
		step = 6 * 3600
	}
	if span > 40*24*time.Hour {
		step = 86400
	}
	o.StepSec = step
	rows, err := l.Conn.Query(ctx, fmt.Sprintf("SELECT toStartOfInterval(hour, INTERVAL %d SECOND) AS b, toString(category) AS k, sum(cnt) FROM %s WHERE %s GROUP BY b, k ORDER BY b", step, l.t("logs_hourly"), w), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b Bucket
		if err := rows.Scan(&b.T, &b.Key, &b.Count); err != nil {
			return nil, err
		}
		o.Timeline = append(o.Timeline, b)
	}
	return o, rows.Err()
}

// ---------------------------------------------------------------- storage info

type StorageInfo struct {
	Rows           uint64    `json:"rows"`
	BytesOnDisk    uint64    `json:"bytes_on_disk"`
	BytesRaw       uint64    `json:"bytes_uncompressed"`
	Oldest         time.Time `json:"oldest"`
	Newest         time.Time `json:"newest"`
	Partitions     uint64    `json:"partitions"`
	ClickHouseVers string    `json:"clickhouse_version"`
}

func (l *Logs) Storage(ctx context.Context) (*StorageInfo, error) {
	s := &StorageInfo{}
	err := l.Conn.QueryRow(ctx, `SELECT sum(rows), sum(bytes_on_disk), sum(data_uncompressed_bytes), uniqExact(partition)
		FROM `+l.system("parts")+` WHERE active AND database = ? AND table = ?`, l.db, l.localName("logs")).Scan(&s.Rows, &s.BytesOnDisk, &s.BytesRaw, &s.Partitions)
	if err != nil {
		return nil, err
	}
	if s.Rows > 0 {
		l.Conn.QueryRow(ctx, "SELECT min(ts), max(ts) FROM "+l.t("logs")).Scan(&s.Oldest, &s.Newest)
	}
	l.Conn.QueryRow(ctx, "SELECT version()").Scan(&s.ClickHouseVers)
	return s, nil
}

func (l *Logs) MaxReceived(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := l.Conn.QueryRow(ctx, "SELECT max(received) FROM "+l.t("logs")).Scan(&t)
	return t, err
}

// ---------------------------------------------------------------- backup helpers (HTTP interface)

func (l *Logs) httpReq(ctx context.Context, q string, body io.Reader) (*http.Response, error) {
	u, err := url.Parse(l.cfg.HTTPURL)
	if err != nil {
		return nil, err
	}
	v := url.Values{}
	v.Set("query", q)
	v.Set("database", l.db)
	v.Set("enable_http_compression", "1")
	u.RawQuery = v.Encode()
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(l.cfg.Username, l.cfg.Password)
	if body != nil {
		req.Header.Set("Content-Encoding", "gzip")
	} else {
		// Set explicitly so Go hands us the gzip stream untouched.
		req.Header.Set("Accept-Encoding", "gzip")
	}
	resp, err := l.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("clickhouse http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

// Export streams rows with received in (after, until] as gzip-compressed
// ClickHouse Native format to w.
func (l *Logs) Export(ctx context.Context, w io.Writer, after, until time.Time) (int64, error) {
	q := fmt.Sprintf("SELECT * FROM logs WHERE received > toDateTime64('%s', 3, 'UTC') AND received <= toDateTime64('%s', 3, 'UTC') FORMAT Native",
		after.UTC().Format("2006-01-02 15:04:05.000"), until.UTC().Format("2006-01-02 15:04:05.000"))
	resp, err := l.httpReq(ctx, q, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return io.Copy(w, resp.Body)
}

// Import loads a gzip-compressed Native stream produced by Export.
func (l *Logs) Import(ctx context.Context, r io.Reader) error {
	resp, err := l.httpReq(ctx, "INSERT INTO logs FORMAT Native", r)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (l *Logs) Truncate(ctx context.Context) error {
	if err := l.Conn.Exec(ctx, "TRUNCATE TABLE "+l.local("logs")+l.onCluster()); err != nil {
		return err
	}
	return l.Conn.Exec(ctx, "TRUNCATE TABLE "+l.local("logs_hourly")+l.onCluster())
}

func (l *Logs) CountReceivedBetween(ctx context.Context, after, until time.Time) (uint64, error) {
	var n uint64
	err := l.Conn.QueryRow(ctx, "SELECT count() FROM "+l.t("logs")+" WHERE received > ? AND received <= ?", after, until).Scan(&n)
	return n, err
}

type Device struct {
	Hostname string    `json:"hostname"`
	Model    string    `json:"model"`
	MAC      string    `json:"mac"`
	Version  string    `json:"version"`
	SourceIP string    `json:"source_ip"`
	Site     string    `json:"site"`
	LastSeen time.Time `json:"last_seen"`
	Count    uint64    `json:"count"`
	Errors   uint64    `json:"errors"`
}

// Devices lists UniFi devices (gateways, APs, switches) seen in the window.
func (l *Logs) Devices(ctx context.Context, from time.Time) ([]Device, error) {
	rows, err := l.Conn.Query(ctx, `SELECT hostname, anyLast(device_model), device_mac, anyLast(device_version), anyLast(source_ip), anyLast(site), max(ts), count(), countIf(severity <= 3)
		FROM `+l.t("logs")+` WHERE ts >= ? GROUP BY hostname, device_mac ORDER BY hostname LIMIT 2000`, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.Hostname, &d.Model, &d.MAC, &d.Version, &d.SourceIP, &d.Site, &d.LastSeen, &d.Count, &d.Errors); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

type SiteStat struct {
	Site     string    `json:"site"`
	Total    uint64    `json:"total"`
	Blocked  uint64    `json:"blocked"`
	Threats  uint64    `json:"threats"`
	Errors   uint64    `json:"errors"`
	LastHour time.Time `json:"last_hour"`
}

// SiteStats sums the hourly rollup per site since from.
func (l *Logs) SiteStats(ctx context.Context, from time.Time) ([]SiteStat, error) {
	rows, err := l.Conn.Query(ctx, `SELECT toString(site), sum(cnt), sumIf(cnt, action IN ('block','reject')), sumIf(cnt, category='ips'), sumIf(cnt, severity<=3), max(hour)
		FROM `+l.t("logs_hourly")+` WHERE hour >= toStartOfHour(?) GROUP BY site`, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SiteStat{}
	for rows.Next() {
		var s SiteStat
		if err := rows.Scan(&s.Site, &s.Total, &s.Blocked, &s.Threats, &s.Errors, &s.LastHour); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- alert rules

// AlertFields are the fields an alert rule can group by or count distinct
// values of.
func AlertFields() []string {
	out := make([]string, 0, len(topCols))
	for k := range topCols {
		out = append(out, k)
	}
	return out
}

func AlertFieldOK(f string) bool { _, ok := topCols[f]; return ok }

// AlertQuery evaluates an alert rule: the matching events of the window
// [WindowStart, To], and of the baseline [From, WindowStart) before it.
type AlertQuery struct {
	SearchReq
	WindowStart time.Time
	// GroupBy splits the counts per value of a field (see AlertFields).
	GroupBy string
	// Distinct counts distinct values of a field instead of events.
	Distinct string
	// Fewest orders the groups by the lowest window value first.
	Fewest bool
}

// AlertGroup is the window value (events or distinct values) and the
// baseline event count of one group; Key is "" without GroupBy.
type AlertGroup struct {
	Key      string `json:"key"`
	Current  uint64 `json:"current"`
	Baseline uint64 `json:"baseline"`
	// First is the oldest baseline event, to tell how much history exists.
	First time.Time `json:"-"`
}

func (l *Logs) AlertStats(ctx context.Context, q AlertQuery, limit int) ([]AlertGroup, error) {
	w, args, err := l.where(q.SearchReq)
	if err != nil {
		return nil, err
	}
	key := "''"
	if q.GroupBy != "" {
		c, ok := topCols[q.GroupBy]
		if !ok {
			return nil, fmt.Errorf("unknown field %q", q.GroupBy)
		}
		key = c
	}
	cur := "countIf(ts >= ?)"
	if q.Distinct != "" {
		c, ok := topCols[q.Distinct]
		if !ok {
			return nil, fmt.Errorf("unknown field %q", q.Distinct)
		}
		cur = fmt.Sprintf("uniqExactIf(%s, ts >= ? AND %s NOT IN ('', '0', '|'))", c, c)
	}
	having := ""
	if q.GroupBy != "" {
		having = "HAVING k NOT IN ('', '0', '|')"
	}
	order := "DESC"
	if q.Fewest {
		order = "ASC"
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	sql := fmt.Sprintf("SELECT %s AS k, %s AS cur, countIf(ts < ?) AS base, minIf(ts, ts < ?) FROM %s WHERE %s GROUP BY k %s ORDER BY cur %s, k LIMIT %d",
		key, cur, l.t("logs"), w, having, order, limit)
	rows, err := l.Conn.Query(ctx, sql, append([]any{q.WindowStart, q.WindowStart, q.WindowStart}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlertGroup{}
	for rows.Next() {
		var g AlertGroup
		if err := rows.Scan(&g.Key, &g.Current, &g.Baseline, &g.First); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
