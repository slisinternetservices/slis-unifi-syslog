// Package probeproto is the wire protocol between remote probes and the
// main server. Probes always open the connection (HTTPS to the main
// server's web port), so no inbound port is needed at a site.
//
//	POST /api/probe/v1/enroll      one-time code -> probe key
//	POST /api/probe/v1/heartbeat   status -> settings and update offer
//	POST /api/probe/v1/ingest      gzip NDJSON batch of Records
//	GET  /api/probe/v1/download    probe release tarball (?arch=arm64)
package probeproto

import (
	"encoding/base64"
	"errors"
	"strings"
)

const Prefix = "/api/probe/v1"

// KeyPrefix starts every probe key (the credential a probe gets at
// enrollment); EnrollPrefix starts an enrollment key.
const (
	KeyPrefix    = "ussp_"
	EnrollPrefix = "uss1_"
)

// HeaderEpoch identifies the probe's record numbering: a probe whose queue
// was wiped starts a new epoch and its sequence numbers restart at 1.
const HeaderEpoch = "X-USS-Probe-Epoch"

// Record is one received syslog message, as buffered on the probe and sent
// to the server. Seq increases by one per record within an epoch, so the
// server can drop records it already has after a retried batch.
type Record struct {
	Seq   uint64 `json:"s"`
	TS    int64  `json:"t"` // receive time, Unix nanoseconds
	IP    string `json:"i"` // sender
	Proto string `json:"p,omitempty"`
	Data  string `json:"d"`
}

// IngestResponse confirms a batch: every record up to Seq is stored.
type IngestResponse struct {
	Accepted int    `json:"accepted"`
	Seq      uint64 `json:"seq"`
}

// Settings are managed on the main server and applied by the probe.
type Settings struct {
	UDP string `json:"udp"`
	TCP string `json:"tcp"`
	// BufferMB limits the on-disk buffer; the oldest messages go first.
	BufferMB int `json:"buffer_mb"`
	// BufferHours drops buffered messages older than this (0 = keep).
	BufferHours int    `json:"buffer_hours"`
	LogLevel    string `json:"log_level"`
}

func DefaultSettings() Settings {
	return Settings{UDP: ":514", TCP: ":514", BufferMB: 1024, BufferHours: 168, LogLevel: "info"}
}

// Normalize fills in defaults for missing values.
func (s Settings) Normalize() Settings {
	d := DefaultSettings()
	if s.BufferMB <= 0 {
		s.BufferMB = d.BufferMB
	}
	if s.BufferHours < 0 {
		s.BufferHours = d.BufferHours
	}
	switch s.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		s.LogLevel = d.LogLevel
	}
	return s
}

type EnrollRequest struct {
	Code     string `json:"code"`
	Hostname string `json:"hostname"`
	Version  string `json:"version"`
	Arch     string `json:"arch"`
	OS       string `json:"os"`
}

type EnrollResponse struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Site     string   `json:"site"`
	Key      string   `json:"key"`
	Settings Settings `json:"settings"`
}

// Status is what a probe reports with every heartbeat.
type Status struct {
	Version  string   `json:"version"`
	Arch     string   `json:"arch"`
	OS       string   `json:"os"`
	Hostname string   `json:"hostname"`
	IPs      []string `json:"ips"`
	Uptime   int64    `json:"uptime"` // seconds since the probe started

	Received uint64  `json:"received"` // messages since start
	Rate     float64 `json:"rate"`     // messages per second, last minute
	Sent     uint64  `json:"sent"`     // messages delivered since start
	// Buffer is what waits on disk for delivery.
	BufferRecords int64  `json:"buffer_records"`
	BufferBytes   int64  `json:"buffer_bytes"`
	BufferMax     int64  `json:"buffer_max"`
	Dropped       uint64 `json:"dropped"` // lost because the buffer was full
	Expired       uint64 `json:"expired"` // older than the buffer age limit
	Oldest        int64  `json:"oldest"`  // Unix time of the oldest buffered message, 0 = empty
	// Devices is the number of distinct senders in the last 24 hours.
	Devices int `json:"devices"`

	CPU       float64 `json:"cpu"` // percent
	Load1     float64 `json:"load1"`
	MemUsed   uint64  `json:"mem_used"`
	MemTotal  uint64  `json:"mem_total"`
	DiskUsed  uint64  `json:"disk_used"`
	DiskTotal uint64  `json:"disk_total"`

	Listen    string `json:"listen"` // effective "udp :514, tcp :514"
	LastError string `json:"last_error,omitempty"`
	// Update reports the outcome of the last update attempt.
	Update *UpdateReport `json:"update,omitempty"`
}

// UpdateReport tells the server how the last update went: "installed"
// (running the new version), "rolled_back" (the new version did not start)
// or "failed" (download or verification failed, nothing changed).
type UpdateReport struct {
	State string `json:"state"`
	From  string `json:"from"`
	To    string `json:"to"`
	Error string `json:"error,omitempty"`
	At    int64  `json:"at"`
}

type HeartbeatResponse struct {
	Name          string   `json:"name"`
	Site          string   `json:"site"`
	ServerVersion string   `json:"server_version"`
	Settings      Settings `json:"settings"`
	// Update asks the probe to install another version.
	Update *UpdateOffer `json:"update,omitempty"`
}

type UpdateOffer struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	// Path is fetched from the main server with the probe key.
	Path string `json:"path"`
}

// AssetName is the release tarball of the probe for an architecture.
func AssetName(arch string) string { return "unify-syslog-probe_linux_" + arch + ".tar.gz" }

// Archs are the architectures probes are built for.
var Archs = []string{"amd64", "arm64", "arm"}

// EnrollKey bundles everything a new probe needs: the server URL, the
// one-time code and the server's public key pin (base64 SHA-256 of the
// SubjectPublicKeyInfo, as curl --pinnedpubkey uses it). The pin lets the
// probe trust the server's self-signed certificate.
type EnrollKey struct {
	URL  string
	Code string
	Pin  string
}

func (k EnrollKey) String() string {
	return EnrollPrefix + base64.RawURLEncoding.EncodeToString([]byte(k.URL+"|"+k.Code+"|"+k.Pin))
}

// ParseEnrollKey decodes a key made by EnrollKey.String.
func ParseEnrollKey(s string) (EnrollKey, error) {
	s = strings.TrimSpace(s)
	raw, ok := strings.CutPrefix(s, EnrollPrefix)
	if !ok {
		return EnrollKey{}, errors.New("not an enrollment key (it starts with " + EnrollPrefix + ")")
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(raw, "="))
	if err != nil {
		return EnrollKey{}, errors.New("enrollment key is damaged")
	}
	parts := strings.Split(string(b), "|")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return EnrollKey{}, errors.New("enrollment key is damaged")
	}
	return EnrollKey{URL: parts[0], Code: parts[1], Pin: parts[2]}, nil
}
