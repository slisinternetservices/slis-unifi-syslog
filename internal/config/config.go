// Package config loads the static bootstrap configuration from YAML.
// Everything that can be changed from the web UI lives in the settings
// table of the metadata database instead.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"
)

const DefaultPath = "/etc/unify-syslog/config.yaml"

type Config struct {
	HTTPListen string `yaml:"http_listen"`
	// TLSCert and TLSKey replace the self-signed certificate the web port
	// also serves HTTPS with (used by remote probes).
	TLSCert    string     `yaml:"tls_cert"`
	TLSKey     string     `yaml:"tls_key"`
	DataDir    string     `yaml:"data_dir"`
	Syslog     Syslog     `yaml:"syslog"`
	ClickHouse ClickHouse `yaml:"clickhouse"`
	Ingest     Ingest     `yaml:"ingest"`
	// ManagedDir holds ClickHouse configuration fragments this server
	// writes (cold storage disks); the installer links them into the local
	// ClickHouse server's config.d.
	ManagedDir string `yaml:"managed_dir"`
	// HA is this node's high availability setup. It is node-local: it
	// lives in node.yaml and is never synced to the peer.
	HA HA `yaml:"-"`
	// NodeOverride is set when node.yaml replaced the ClickHouse connection.
	NodeOverride bool `yaml:"-"`
}

type Syslog struct {
	UDP string `yaml:"udp"`
	TCP string `yaml:"tcp"`
	// Size of the kernel receive buffer requested for the UDP socket.
	UDPReadBuffer int `yaml:"udp_read_buffer"`
}

type ClickHouse struct {
	Addr string `yaml:"addr" json:"addr"` // native protocol, host:9000 (9440 with TLS)
	// Addrs are more servers of the same cluster or replicas, tried in
	// order when the first cannot be reached.
	Addrs    []string `yaml:"addrs,omitempty" json:"addrs"`
	HTTPURL  string   `yaml:"http_url" json:"http_url"` // http interface, used for backup export/import
	Database string   `yaml:"database" json:"database"`
	Username string   `yaml:"username" json:"username"`
	Password string   `yaml:"password" json:"password"`
	// Secure uses TLS for the native protocol.
	Secure     bool   `yaml:"secure,omitempty" json:"secure"`
	SkipVerify bool   `yaml:"skip_verify,omitempty" json:"skip_verify"`
	CACert     string `yaml:"ca_cert,omitempty" json:"ca_cert"` // PEM file with the CA that signed the server certificate
	// Cluster is the name of a cluster in the servers' remote_servers.
	// When set, logs are stored in replicated tables on every node and
	// read through Distributed tables.
	Cluster string `yaml:"cluster,omitempty" json:"cluster"`
}

// HA is the high availability setup of one node: two servers share a
// virtual IP (keepalived) and an external ClickHouse; the node holding the
// virtual IP is active and the other follows its settings.
type HA struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	VIP     string `yaml:"vip" json:"vip"` // virtual IP, without prefix length
	// NodeName labels this node in the interface.
	NodeName string `yaml:"node_name" json:"node_name"`
	// SelfURL is how the peer reaches this node (https://host:8080).
	SelfURL string `yaml:"self_url" json:"self_url"`
	PeerURL string `yaml:"peer_url" json:"peer_url"`
	// PeerPin is the SHA-256 pin of the peer's TLS certificate.
	PeerPin string `yaml:"peer_pin" json:"peer_pin"`
	// Secret authenticates the two nodes to each other.
	Secret string `yaml:"secret" json:"-"`
	// SyncSeconds is how often the standby checks the active node for
	// changed settings.
	SyncSeconds int `yaml:"sync_seconds" json:"sync_seconds"`
	// Keepalived settings, used to generate keepalived.conf.
	Interface string `yaml:"interface" json:"interface"`
	Priority  int    `yaml:"priority" json:"priority"`
	RouterID  int    `yaml:"router_id" json:"router_id"`
}

// Node is node.yaml in the data directory: settings changed from the web
// interface that belong to this server only. The service cannot write
// /etc, so they live next to the data.
type Node struct {
	ClickHouse *ClickHouse `yaml:"clickhouse,omitempty"`
	HA         *HA         `yaml:"ha,omitempty"`
}

type Ingest struct {
	QueueSize     int `yaml:"queue_size"`
	Workers       int `yaml:"workers"`
	BatchSize     int `yaml:"batch_size"`
	FlushInterval int `yaml:"flush_interval_ms"`
}

func Default() *Config {
	return &Config{
		HTTPListen: ":8080",
		DataDir:    "/var/lib/unify-syslog",
		Syslog:     Syslog{UDP: ":514", TCP: ":514", UDPReadBuffer: 8 << 20},
		ClickHouse: ClickHouse{
			Addr:     "127.0.0.1:9000",
			HTTPURL:  "http://127.0.0.1:8123",
			Database: "unify_syslog",
			Username: "default",
		},
		Ingest:     Ingest{QueueSize: 200000, Workers: 4, BatchSize: 20000, FlushInterval: 1000},
		ManagedDir: "/var/lib/unify-syslog-ch",
	}
}

func Load(path string) (*Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if v := os.Getenv("UNIFY_SYSLOG_HTTP"); v != "" {
		cfg.HTTPListen = v
	}
	n, err := cfg.LoadNode()
	if err != nil {
		return nil, err
	}
	if n.ClickHouse != nil {
		cfg.ClickHouse = *n.ClickHouse
		cfg.NodeOverride = true
	}
	if n.HA != nil {
		cfg.HA = *n.HA
	}
	return cfg, nil
}

func (c *Config) NodePath() string { return filepath.Join(c.DataDir, "node.yaml") }

// LoadNode reads node.yaml; a missing file is an empty Node.
func (c *Config) LoadNode() (Node, error) {
	var n Node
	b, err := os.ReadFile(c.NodePath())
	if os.IsNotExist(err) {
		return n, nil
	}
	if err != nil {
		return n, err
	}
	if err := yaml.Unmarshal(b, &n); err != nil {
		return n, fmt.Errorf("parse %s: %w", c.NodePath(), err)
	}
	return n, nil
}

// SaveNode writes node.yaml atomically. It holds passwords, so only the
// service user can read it.
func (c *Config) SaveNode(n Node) error {
	b, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	b = append([]byte("# Node-local settings, managed from the web interface.\n"), b...)
	tmp := c.NodePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.NodePath())
}

// Addresses returns every native protocol address, the primary first.
func (c ClickHouse) Addresses() []string {
	out := []string{}
	for _, a := range append([]string{c.Addr}, c.Addrs...) {
		if a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

func (c *Config) MetaDBPath() string    { return filepath.Join(c.DataDir, "meta.db") }
func (c *Config) BackupDir() string     { return filepath.Join(c.DataDir, "backups") }
func (c *Config) SecretKeyPath() string { return filepath.Join(c.DataDir, "secret.key") }
