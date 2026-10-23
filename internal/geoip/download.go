package geoip

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

// Providers.
const (
	DBIP    = "dbip"    // DB-IP Lite, free, no account (CC BY 4.0)
	MaxMind = "maxmind" // MaxMind GeoLite2, free account and license key
)

// Source says where databases come from.
type Source struct {
	Provider   string
	AccountID  string
	LicenseKey string
}

// Endpoints, overridable in tests.
var (
	DBIPURL    = "https://download.db-ip.com/free/dbip-%s-lite-%s.mmdb.gz"
	MaxMindURL = "https://download.maxmind.com/geoip/databases/%s/download?suffix=tar.gz"
)

// Download fetches the country and ASN databases into dir. Each file is
// verified before it replaces the current one, so a failed download never
// leaves a broken database behind.
func Download(ctx context.Context, c *http.Client, src Source, dir string, now time.Time) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	type job struct{ kind, file string }
	jobs := []job{{"country", CountryFile}, {"asn", ASNFile}}
	for _, j := range jobs {
		var body io.ReadCloser
		var err error
		switch src.Provider {
		case MaxMind:
			if src.AccountID == "" || src.LicenseKey == "" {
				return errors.New("MaxMind needs an account ID and a license key")
			}
			edition := map[string]string{"country": "GeoLite2-Country", "asn": "GeoLite2-ASN"}[j.kind]
			body, err = fetchMaxMind(ctx, c, edition, src)
		default:
			body, err = fetchDBIP(ctx, c, j.kind, now)
		}
		if err != nil {
			return fmt.Errorf("%s database: %w", j.kind, err)
		}
		err = install(body, filepath.Join(dir, j.file))
		body.Close()
		if err != nil {
			return fmt.Errorf("%s database: %w", j.kind, err)
		}
	}
	return nil
}

// fetchDBIP tries this month's release first; early in the month it may not
// be published yet, so it falls back to last month's.
func fetchDBIP(ctx context.Context, c *http.Client, kind string, now time.Time) (io.ReadCloser, error) {
	var lastErr error
	for _, t := range []time.Time{now, now.AddDate(0, -1, 0)} {
		resp, err := get(ctx, c, fmt.Sprintf(DBIPURL, kind, t.UTC().Format("2006-01")), nil)
		if err != nil {
			lastErr = err
			continue
		}
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			resp.Body.Close()
			return nil, err
		}
		return readCloser{gz, resp.Body}, nil
	}
	return nil, lastErr
}

func fetchMaxMind(ctx context.Context, c *http.Client, edition string, src Source) (io.ReadCloser, error) {
	resp, err := get(ctx, c, fmt.Sprintf(MaxMindURL, edition), func(r *http.Request) { r.SetBasicAuth(src.AccountID, src.LicenseKey) })
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		resp.Body.Close()
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			resp.Body.Close()
			if err == io.EOF {
				return nil, errors.New("archive contains no .mmdb file")
			}
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && strings.HasSuffix(h.Name, ".mmdb") {
			return readCloser{tr, resp.Body}, nil
		}
	}
}

func get(ctx context.Context, c *http.Client, url string, prep func(*http.Request)) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "unify-syslog-server")
	if prep != nil {
		prep(req)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, errors.New("the account ID or license key was rejected")
		}
		return nil, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

type readCloser struct {
	io.Reader
	c io.Closer
}

func (r readCloser) Close() error { return r.c.Close() }

const maxDBSize = 512 << 20

func install(r io.Reader, dst string) error {
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, maxDBSize+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxDBSize {
		err = errors.New("database file is too large")
	}
	if err == nil {
		var db *maxminddb.Reader
		if db, err = maxminddb.Open(tmp); err == nil {
			db.Close()
		} else {
			err = fmt.Errorf("not a valid database: %w", err)
		}
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
