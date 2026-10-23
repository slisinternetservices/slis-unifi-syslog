// Package geoip looks up the country and autonomous system of public IP
// addresses in MaxMind DB files (DB-IP Lite or MaxMind GeoLite2) and keeps
// those files up to date.
package geoip

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

const (
	CountryFile = "country.mmdb"
	ASNFile     = "asn.mmdb"
)

// Info is what is known about one address. Empty fields mean unknown.
type Info struct {
	Country string // ISO 3166-1 alpha-2, e.g. "NL"
	ASN     uint32
	Org     string
}

// DB holds the opened country and ASN databases; either may be missing.
type DB struct {
	country *maxminddb.Reader
	asn     *maxminddb.Reader
}

// Open loads the databases in dir. A directory without any database yields
// an error so callers can tell "not downloaded yet" apart from "no match".
func Open(dir string) (*DB, error) {
	d := &DB{}
	var err error
	if p := filepath.Join(dir, CountryFile); exists(p) {
		if d.country, err = maxminddb.Open(p); err != nil {
			return nil, err
		}
	}
	if p := filepath.Join(dir, ASNFile); exists(p) {
		if d.asn, err = maxminddb.Open(p); err != nil {
			d.Close()
			return nil, err
		}
	}
	if d.country == nil && d.asn == nil {
		return nil, errors.New("no GeoIP database downloaded yet")
	}
	return d, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func (d *DB) Close() {
	if d.country != nil {
		d.country.Close()
	}
	if d.asn != nil {
		d.asn.Close()
	}
}

type countryRecord struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	// Anycast and satellite ranges often only carry a registered country.
	Registered struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"registered_country"`
}

type asnRecord struct {
	Number uint32 `maxminddb:"autonomous_system_number"`
	Org    string `maxminddb:"autonomous_system_organization"`
}

// Lookup returns what the databases know about ip. Private and reserved
// addresses are never looked up.
func (d *DB) Lookup(ip netip.Addr) Info {
	var out Info
	if d == nil || !Public(ip) {
		return out
	}
	if d.country != nil {
		var r countryRecord
		if err := d.country.Lookup(ip).Decode(&r); err == nil {
			out.Country = r.Country.ISOCode
			if out.Country == "" {
				out.Country = r.Registered.ISOCode
			}
		}
	}
	if d.asn != nil {
		var r asnRecord
		if err := d.asn.Lookup(ip).Decode(&r); err == nil {
			out.ASN, out.Org = r.Number, r.Org
		}
	}
	return out
}

// Meta describes one database file for the status page.
type Meta struct {
	Type    string    `json:"type"`
	Built   time.Time `json:"built"`
	Updated time.Time `json:"updated"`
	Size    int64     `json:"size"`
}

// Describe lists the database files in dir.
func Describe(dir string) []Meta {
	out := []Meta{}
	for _, f := range []string{CountryFile, ASNFile} {
		p := filepath.Join(dir, f)
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		m := Meta{Updated: st.ModTime(), Size: st.Size()}
		if r, err := maxminddb.Open(p); err == nil {
			m.Type = r.Metadata.DatabaseType
			m.Built = time.Unix(int64(r.Metadata.BuildEpoch), 0)
			r.Close()
		}
		out = append(out, m)
	}
	return out
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// Public reports whether ip is a globally routed unicast address.
func Public(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !cgnat.Contains(ip)
}
