package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/config"
	"github.com/rhisijmuiden/unify-syslog-server/internal/unifi"
)

// These tests need a running ClickHouse server; they are skipped unless
// USS_TEST_CLICKHOUSE is set to its native address (user and password in
// USS_TEST_CH_USER and USS_TEST_CH_PASSWORD). USS_TEST_CH_CLUSTER names a
// cluster for the cluster test, USS_TEST_CH_MANAGED a managed directory the
// server includes and USS_TEST_S3 an S3 endpoint with a bucket "coldbucket"
// for the cold storage test.
func testCH(t *testing.T, db, cluster string) *Logs {
	addr := os.Getenv("USS_TEST_CLICKHOUSE")
	if addr == "" {
		t.Skip("USS_TEST_CLICKHOUSE not set")
	}
	cfg := config.ClickHouse{Addr: addr, HTTPURL: "http://" + strings.Replace(addr, ":9000", ":8123", 1), Database: db,
		Username: os.Getenv("USS_TEST_CH_USER"), Password: os.Getenv("USS_TEST_CH_PASSWORD"), Cluster: cluster}
	ctx := context.Background()
	conn, err := Connect(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	on := ""
	if cluster != "" {
		on = " ON CLUSTER " + cluster + " SYNC"
	}
	if err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+db+on); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	l, err := OpenLogs(ctx, cfg, 90)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Conn.Close() })
	return l
}

func insertTest(t *testing.T, l *Logs, n int, age time.Duration) {
	evs := make([]*unifi.Event, n)
	now := time.Now().UTC()
	for i := range evs {
		ts := now.Add(-age).Add(time.Duration(i) * time.Second)
		evs[i] = &unifi.Event{TS: ts, Received: ts, Site: "hq", Category: "firewall", Message: "test", Action: "block"}
	}
	if err := l.Insert(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}

func TestClusterLogStore(t *testing.T) {
	cluster := os.Getenv("USS_TEST_CH_CLUSTER")
	if cluster == "" {
		t.Skip("USS_TEST_CH_CLUSTER not set")
	}
	l := testCH(t, "uss_test_cluster", cluster)
	ctx := context.Background()
	insertTest(t, l, 50, time.Hour)
	// Inserts through the Distributed table arrive asynchronously.
	deadline := time.Now().Add(20 * time.Second)
	for {
		n, err := l.Count(ctx, SearchReq{From: time.Now().Add(-2 * time.Hour), To: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		if n == 50 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("count %d, want 50", n)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err := l.SetRetention(ctx, 30, map[string]int{"wifi": 7}); err != nil {
		t.Fatal(err)
	}
	if h := l.Health(ctx); !h.OK || h.Parts == 0 {
		t.Fatalf("health %+v", h)
	}
	if st, err := l.Storage(ctx); err != nil || st.Rows != 50 {
		t.Fatalf("storage %+v %v", st, err)
	}
	if err := l.Truncate(ctx); err != nil {
		t.Fatal(err)
	}
	// Opening again keeps working, and a single-server database is refused.
	cfg := l.Config()
	if _, err := OpenLogs(ctx, cfg, 90); err != nil {
		t.Fatal(err)
	}
	single := testCH(t, "uss_test_single", "")
	cfg = single.Config()
	cfg.Cluster = cluster
	if _, err := OpenLogs(ctx, cfg, 90); err == nil || !strings.Contains(err.Error(), "single server") {
		t.Fatalf("want refusal, got %v", err)
	}
}

func TestColdStorage(t *testing.T) {
	dir, s3 := os.Getenv("USS_TEST_CH_MANAGED"), os.Getenv("USS_TEST_S3")
	if dir == "" || s3 == "" {
		t.Skip("USS_TEST_CH_MANAGED or USS_TEST_S3 not set")
	}
	l := testCH(t, "uss_test_cold", "")
	ctx := context.Background()
	l.InitRetention(90, nil, ColdStorage{})
	insertTest(t, l, 100, 40*24*time.Hour)
	insertTest(t, l, 100, time.Hour)
	c := ColdStorage{Enabled: true, Mode: "s3", AfterDays: 30, Endpoint: s3, Bucket: "coldbucket", Prefix: "test/", AccessKey: "a", SecretKey: "b", CacheGB: 1}
	if err := c.Validate(90); err != nil {
		t.Fatal(err)
	}
	if !ManagedAvailable(dir) {
		t.Fatal("managed dir not writable")
	}
	if err := WriteManaged(dir, ManagedXML(c)); err != nil {
		t.Fatal(err)
	}
	if err := l.ReloadConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if !l.HasDisk(ctx, ColdDisk) {
		t.Fatal("disk not loaded")
	}
	if err := l.SetCold(ctx, ColdPolicy, ColdVolume, 30); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		disks, err := l.TableDisks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var cold uint64
		for _, d := range disks {
			if d.Disk != "default" {
				cold += d.Rows
			}
		}
		if cold == 100 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cold rows %d: %+v", cold, disks)
		}
		time.Sleep(time.Second)
	}
	n, err := l.Count(ctx, SearchReq{From: time.Now().Add(-50 * 24 * time.Hour), To: time.Now()})
	if err != nil || n != 200 {
		t.Fatalf("count %d %v", n, err)
	}
	var ttl string
	l.Conn.QueryRow(ctx, "SELECT create_table_query FROM system.tables WHERE database = 'uss_test_cold' AND name = 'logs'").Scan(&ttl)
	if !strings.Contains(ttl, "TO VOLUME 'cold'") {
		t.Fatalf("ttl: %s", ttl)
	}
	// Changing the retention keeps the move rule.
	if err := l.SetRetention(ctx, 60, nil); err != nil {
		t.Fatal(err)
	}
	l.Conn.QueryRow(ctx, "SELECT create_table_query FROM system.tables WHERE database = 'uss_test_cold' AND name = 'logs'").Scan(&ttl)
	if !strings.Contains(ttl, "TO VOLUME 'cold'") || !strings.Contains(ttl, "toIntervalDay(60)") {
		t.Fatalf("ttl after retention change: %s", ttl)
	}
}
