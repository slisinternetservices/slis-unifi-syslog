package geoip

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
)

// writeDB builds a small MaxMind DB with one network.
func writeDB(t *testing.T, kind string) []byte {
	t.Helper()
	tree, err := mmdbwriter.New(mmdbwriter.Options{DatabaseType: "test-" + kind, RecordSize: 24})
	if err != nil {
		t.Fatal(err)
	}
	_, n, _ := net.ParseCIDR("1.1.1.0/24")
	var v mmdbtype.Map
	if kind == "country" {
		v = mmdbtype.Map{"country": mmdbtype.Map{"iso_code": mmdbtype.String("AU")}}
	} else {
		v = mmdbtype.Map{"autonomous_system_number": mmdbtype.Uint32(13335), "autonomous_system_organization": mmdbtype.String("Cloudflare, Inc.")}
	}
	if err := tree.Insert(n, v); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if _, err := tree.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestLookup(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, CountryFile), writeDB(t, "country"), 0o600)
	os.WriteFile(filepath.Join(dir, ASNFile), writeDB(t, "asn"), 0o600)
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got := db.Lookup(netip.MustParseAddr("1.1.1.1"))
	if got.Country != "AU" || got.ASN != 13335 || got.Org != "Cloudflare, Inc." {
		t.Fatalf("got %+v", got)
	}
	if got := db.Lookup(netip.MustParseAddr("192.168.1.1")); got != (Info{}) {
		t.Fatalf("private address looked up: %+v", got)
	}
	if len(Describe(dir)) != 2 {
		t.Fatal("describe")
	}
}

func TestPublic(t *testing.T) {
	for ip, want := range map[string]bool{"8.8.8.8": true, "10.1.2.3": false, "100.64.1.1": false, "127.0.0.1": false, "169.254.1.1": false, "fe80::1": false, "2606:4700::1111": true, "::ffff:8.8.8.8": true} {
		if got := Public(netip.MustParseAddr(ip)); got != want {
			t.Errorf("Public(%s) = %v", ip, got)
		}
	}
}

func gz(b []byte) []byte {
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	w.Write(b)
	w.Close()
	return out.Bytes()
}

func TestDownloadDBIPFallsBackToLastMonth(t *testing.T) {
	country, asn := gz(writeDB(t, "country")), gz(writeDB(t, "asn"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "2026-10"):
			http.NotFound(w, r) // this month not published yet
		case strings.Contains(r.URL.Path, "country-lite-2026-09"):
			w.Write(country)
		case strings.Contains(r.URL.Path, "asn-lite-2026-09"):
			w.Write(asn)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := DBIPURL
	DBIPURL = srv.URL + "/dbip-%s-lite-%s.mmdb.gz"
	defer func() { DBIPURL = old }()

	dir := t.TempDir()
	if err := Download(context.Background(), srv.Client(), Source{Provider: DBIP}, dir, time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	m := &Manager{Dir: dir}
	m.Load()
	if got := m.Lookup(netip.MustParseAddr("1.1.1.9")); got.Country != "AU" || got.ASN != 13335 {
		t.Fatalf("got %+v", got)
	}
}

func TestDownloadRejectsGarbage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(gz([]byte("not a database"))) }))
	defer srv.Close()
	old := DBIPURL
	DBIPURL = srv.URL + "/%s/%s"
	defer func() { DBIPURL = old }()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, CountryFile), writeDB(t, "country"), 0o600)
	if err := Download(context.Background(), srv.Client(), Source{}, dir, time.Now()); err == nil {
		t.Fatal("garbage accepted")
	}
	// The previous database stays in place.
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadMaxMind(t *testing.T) {
	archive := func(name string, db []byte) []byte {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		tw := tar.NewWriter(zw)
		tw.WriteHeader(&tar.Header{Name: name + "_20261014/", Typeflag: tar.TypeDir, Mode: 0o755})
		tw.WriteHeader(&tar.Header{Name: name + "_20261014/COPYRIGHT.txt", Typeflag: tar.TypeReg, Size: 2, Mode: 0o644})
		tw.Write([]byte("c\n"))
		tw.WriteHeader(&tar.Header{Name: name + "_20261014/" + name + ".mmdb", Typeflag: tar.TypeReg, Size: int64(len(db)), Mode: 0o644})
		tw.Write(db)
		tw.Close()
		zw.Close()
		return b.Bytes()
	}
	country, asn := archive("GeoLite2-Country", writeDB(t, "country")), archive("GeoLite2-ASN", writeDB(t, "asn"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "123" || p != "key" {
			w.WriteHeader(401)
			return
		}
		if strings.Contains(r.URL.Path, "Country") {
			w.Write(country)
		} else {
			w.Write(asn)
		}
	}))
	defer srv.Close()
	old := MaxMindURL
	MaxMindURL = srv.URL + "/%s"
	defer func() { MaxMindURL = old }()
	dir := t.TempDir()
	if err := Download(context.Background(), srv.Client(), Source{Provider: MaxMind, AccountID: "123", LicenseKey: "wrong"}, dir, time.Now()); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("bad key: %v", err)
	}
	if err := Download(context.Background(), srv.Client(), Source{Provider: MaxMind, AccountID: "123", LicenseKey: "key"}, dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := db.Lookup(netip.MustParseAddr("1.1.1.1")); got.Country != "AU" || got.Org == "" {
		t.Fatalf("got %+v", got)
	}
}
