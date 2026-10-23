package api

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/config"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

// storageJob is a running or finished switch to another ClickHouse server.
type storageJob struct {
	mu       sync.Mutex
	Running  bool      `json:"running"`
	Step     string    `json:"step"`
	Copied   uint64    `json:"copied"`
	Total    uint64    `json:"total"`
	Error    string    `json:"error"`
	Finished time.Time `json:"finished"`
}

func (j *storageJob) set(f func(j *storageJob)) {
	j.mu.Lock()
	f(j)
	j.mu.Unlock()
}

func (j *storageJob) snapshot() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	return map[string]any{"running": j.Running, "step": j.Step, "copied": j.Copied, "total": j.Total, "error": j.Error, "finished": j.Finished}
}

// isLocalCH tells whether the log store is the ClickHouse on this machine.
func isLocalCH(c config.ClickHouse) bool {
	if c.Cluster != "" || len(c.Addrs) > 0 {
		return false
	}
	host, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return false
	}
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

func maskCH(c config.ClickHouse) config.ClickHouse {
	if c.Password != "" {
		c.Password = store.Secret
	}
	if c.Addrs == nil {
		c.Addrs = []string{}
	}
	return c
}

func (s *Server) getStorage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cfg := s.Logs.Config()
	out := map[string]any{
		"clickhouse":        maskCH(cfg),
		"node_override":     s.Cfg.NodeOverride,
		"local":             isLocalCH(cfg),
		"managed_available": isLocalCH(cfg) && store.ManagedAvailable(s.Cfg.ManagedDir),
		"cold":              store.MaskColdStorage(s.Meta.ColdStorage()),
		"job":               s.storageJob.snapshot(),
		"retention_days":    s.Meta.General().RetentionDays,
	}
	if st, err := s.Logs.Storage(ctx); err == nil {
		out["storage"] = st
	}
	if d, err := s.Logs.TableDisks(ctx); err == nil {
		out["disks"] = d
	}
	if p, err := s.Logs.Policies(ctx); err == nil {
		out["policies"] = p
	}
	if p, err := s.Logs.StoragePolicy(ctx); err == nil {
		out["policy"] = p
	}
	jsonOut(w, out)
}

// chFromRequest fills in a password left masked by the interface.
func (s *Server) chFromRequest(c config.ClickHouse) config.ClickHouse {
	if c.Password == store.Secret {
		c.Password = s.Logs.Config().Password
	}
	clean := []string{}
	for _, a := range c.Addrs {
		if a != "" {
			clean = append(clean, a)
		}
	}
	c.Addrs = clean
	if c.Database == "" {
		c.Database = "unify_syslog"
	}
	return c
}

func validateCH(c config.ClickHouse) error {
	for _, a := range c.Addresses() {
		if _, _, err := net.SplitHostPort(a); err != nil {
			return fmt.Errorf("address %q must be host:port", a)
		}
	}
	if c.Addr == "" || c.HTTPURL == "" {
		return fmt.Errorf("the native address and the HTTP URL are required")
	}
	if !identOK(c.Database) {
		return fmt.Errorf("invalid database name")
	}
	return nil
}

func identOK(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func (s *Server) testClickHouse(w http.ResponseWriter, r *http.Request) {
	var c config.ClickHouse
	if err := decode(r, &c); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	c = s.chFromRequest(c)
	if err := validateCH(c); err != nil {
		jerr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	conn, err := store.Connect(ctx, c)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	defer conn.Close()
	var version string
	conn.QueryRow(ctx, "SELECT version()").Scan(&version)
	var rows uint64
	conn.QueryRow(ctx, "SELECT sum(rows) FROM system.parts WHERE active AND database = ? AND table IN ('logs', 'logs_local')", c.Database).Scan(&rows)
	out := map[string]any{"version": version, "rows": rows}
	if c.Cluster != "" {
		var nodes uint64
		if err := conn.QueryRow(ctx, "SELECT count() FROM system.clusters WHERE cluster = ?", c.Cluster).Scan(&nodes); err != nil || nodes == 0 {
			jerr(w, 400, fmt.Sprintf("connected (ClickHouse %s), but the server does not know a cluster named %q", version, c.Cluster))
			return
		}
		out["nodes"] = nodes
	}
	jsonOut(w, out)
}

// putClickHouse switches the log store to another ClickHouse server or
// cluster: it creates the tables there, optionally copies the stored logs,
// writes node.yaml and restarts.
func (s *Server) putClickHouse(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClickHouse config.ClickHouse `json:"clickhouse"`
		Migrate    bool              `json:"migrate"`
		// Reset goes back to the connection in config.yaml.
		Reset bool `json:"reset"`
	}
	if err := decode(r, &req); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	node, err := s.Cfg.LoadNode()
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	if req.Reset {
		s.coldOffAfterSwitch()
		node.ClickHouse = nil
		if err := s.Cfg.SaveNode(node); err != nil {
			jerr(w, 500, err.Error())
			return
		}
		s.audit_(r, "storage_clickhouse", "reset to config.yaml")
		jsonOut(w, map[string]bool{"restart": true})
		go func() { time.Sleep(time.Second); s.Restart() }()
		return
	}
	c := s.chFromRequest(req.ClickHouse)
	if err := validateCH(c); err != nil {
		jerr(w, 400, err.Error())
		return
	}
	s.storageJob.mu.Lock()
	if s.storageJob.Running {
		s.storageJob.mu.Unlock()
		jerr(w, 409, "a storage change is already running")
		return
	}
	s.storageJob.Running, s.storageJob.Error, s.storageJob.Step, s.storageJob.Copied, s.storageJob.Total = true, "", "connecting", 0, 0
	s.storageJob.mu.Unlock()
	s.audit_(r, "storage_clickhouse", c.Addr+" "+c.Database)
	go func() {
		err := s.switchClickHouse(c, req.Migrate)
		s.storageJob.set(func(j *storageJob) {
			j.Running, j.Finished = false, time.Now()
			if err != nil {
				j.Error, j.Step = err.Error(), ""
			} else {
				j.Step = "restarting"
			}
		})
		if err != nil {
			s.Log.Error("storage switch", "err", err)
			return
		}
		s.coldOffAfterSwitch()
		node.ClickHouse = &c
		if err := s.Cfg.SaveNode(node); err != nil {
			s.storageJob.set(func(j *storageJob) { j.Error = err.Error() })
			return
		}
		s.Restart()
	}()
	jsonOut(w, map[string]bool{"started": true})
}

// coldOffAfterSwitch turns cold storage off: its disk or storage policy
// belongs to the previous ClickHouse and must be set up again on the new one.
func (s *Server) coldOffAfterSwitch() {
	c := s.Meta.ColdStorage()
	if !c.Enabled {
		return
	}
	c.Enabled = false
	if err := s.Meta.SaveColdStorage(c); err != nil {
		s.Log.Warn("turn off cold storage", "err", err)
	}
}

func (s *Server) switchClickHouse(c config.ClickHouse, migrate bool) error {
	ctx := context.Background()
	g := s.Meta.General()
	dst, err := store.OpenLogs(ctx, c, g.RetentionDays)
	if err != nil {
		return err
	}
	defer dst.Conn.Close()
	if err := dst.SetRetention(ctx, g.RetentionDays, g.CategoryRetention); err != nil {
		return fmt.Errorf("retention: %w", err)
	}
	if !migrate {
		return nil
	}
	src := s.Logs
	var first time.Time
	if err := src.Conn.QueryRow(ctx, "SELECT min(received) FROM "+"`"+src.Config().Database+"`.logs").Scan(&first); err != nil {
		return err
	}
	until := time.Now().UTC()
	total, _ := src.CountReceivedBetween(ctx, first.Add(-time.Millisecond), until)
	s.storageJob.set(func(j *storageJob) { j.Total, j.Step = total, "copying logs" })
	from := first.Add(-time.Millisecond)
	copyRange := func(a, b time.Time) error {
		n, err := src.CountReceivedBetween(ctx, a, b)
		if err != nil || n == 0 {
			return err
		}
		pr, pw := io.Pipe()
		go func() {
			_, err := src.Export(ctx, pw, a, b)
			pw.CloseWithError(err)
		}()
		if err := dst.Import(ctx, pr); err != nil {
			pr.CloseWithError(err)
			return err
		}
		s.storageJob.set(func(j *storageJob) { j.Copied += n })
		return nil
	}
	for from.Before(until) {
		to := from.Add(24 * time.Hour)
		if to.After(until) {
			to = until
		}
		if err := copyRange(from, to); err != nil {
			return fmt.Errorf("copy logs: %w", err)
		}
		from = to
	}
	// Logs that arrived while copying.
	return copyRange(until, time.Now().UTC())
}

// putColdStorage applies cold storage: for the local ClickHouse it writes
// the S3 disk configuration and reloads ClickHouse; then it switches the
// logs table to the tiered policy and sets the move rule.
func (s *Server) putColdStorage(w http.ResponseWriter, r *http.Request) {
	var c store.ColdStorage
	if err := decode(r, &c); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	if c.SecretKey == store.Secret {
		c.SecretKey = s.Meta.ColdStorage().SecretKey
	}
	if err := c.Validate(s.Meta.General().RetentionDays); err != nil {
		jerr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	// ClickHouse loads new disks on a reload, but keeps an existing disk as
	// it was until it restarts.
	old := s.Meta.ColdStorage()
	restartCH := c.Enabled && c.Mode == "s3" && old.Mode == "s3" && s.Logs.HasDisk(ctx, store.ColdDisk) &&
		(old.DiskEndpoint() != c.DiskEndpoint() || old.AccessKey != c.AccessKey || old.SecretKey != c.SecretKey || old.Region != c.Region)
	if !c.Enabled {
		if err := s.Logs.SetCold(ctx, "", "", 0); err != nil {
			jerr(w, 500, err.Error())
			return
		}
	} else {
		policy, volume := c.Policy, c.Volume
		if c.Mode == "s3" {
			if !isLocalCH(s.Logs.Config()) {
				jerr(w, 400, "an S3 disk can only be configured on the local ClickHouse; for an external server or cluster choose an existing storage policy")
				return
			}
			if !store.ManagedAvailable(s.Cfg.ManagedDir) {
				jerr(w, 400, "this installation cannot configure ClickHouse yet; run the installer once more as root to enable it")
				return
			}
			if err := store.WriteManaged(s.Cfg.ManagedDir, store.ManagedXML(c)); err != nil {
				jerr(w, 500, err.Error())
				return
			}
			if err := s.Logs.ReloadConfig(ctx); err != nil {
				s.Log.Warn("clickhouse reload", "err", err)
			}
			deadline := time.Now().Add(30 * time.Second)
			for !s.Logs.HasDisk(ctx, store.ColdCache) {
				if time.Now().After(deadline) {
					jerr(w, 500, "ClickHouse did not load the S3 disk; check the endpoint and keys, and the ClickHouse log (journalctl -u clickhouse-server)")
					return
				}
				time.Sleep(time.Second)
			}
			policy, volume = store.ColdPolicy, store.ColdVolume
		}
		if err := s.Logs.SetCold(ctx, policy, volume, c.AfterDays); err != nil {
			jerr(w, 500, err.Error())
			return
		}
	}
	if err := s.Meta.SaveColdStorage(c); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "storage_cold", fmt.Sprintf("enabled=%v mode=%s after=%d", c.Enabled, c.Mode, c.AfterDays))
	jsonOut(w, map[string]any{"cold": store.MaskColdStorage(c), "restart_clickhouse": restartCH})
}
