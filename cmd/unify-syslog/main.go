// Command unify-syslog is a central syslog server optimised for Ubiquiti UniFi.
//
//	unify-syslog [serve]                          run the server
//	unify-syslog user add <name> [--admin]        create a local user (password from stdin or generated)
//	unify-syslog user passwd <name>               reset a password
//	unify-syslog update [--check]                 update to the latest release on the configured channel
//	unify-syslog update rollback [version]        switch back to a kept previous version
//	unify-syslog simulate [--target 127.0.0.1:514] [--rate 200] [--duration 60s] [--sites 3]
//	unify-syslog version
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/alerts"
	"github.com/rhisijmuiden/unify-syslog-server/internal/api"
	"github.com/rhisijmuiden/unify-syslog-server/internal/auth"
	"github.com/rhisijmuiden/unify-syslog-server/internal/backup"
	"github.com/rhisijmuiden/unify-syslog-server/internal/config"
	"github.com/rhisijmuiden/unify-syslog-server/internal/enrich"
	"github.com/rhisijmuiden/unify-syslog-server/internal/forward"
	"github.com/rhisijmuiden/unify-syslog-server/internal/geoip"
	"github.com/rhisijmuiden/unify-syslog-server/internal/ha"
	"github.com/rhisijmuiden/unify-syslog-server/internal/ingest"
	"github.com/rhisijmuiden/unify-syslog-server/internal/mail"
	"github.com/rhisijmuiden/unify-syslog-server/internal/notify"
	"github.com/rhisijmuiden/unify-syslog-server/internal/probehub"
	"github.com/rhisijmuiden/unify-syslog-server/internal/simulate"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/syslogd"
	"github.com/rhisijmuiden/unify-syslog-server/internal/sysstatus"
	"github.com/rhisijmuiden/unify-syslog-server/internal/tlscert"
	"github.com/rhisijmuiden/unify-syslog-server/internal/tlsmux"
	"github.com/rhisijmuiden/unify-syslog-server/internal/update"
	"github.com/rhisijmuiden/unify-syslog-server/internal/version"
	"github.com/rhisijmuiden/unify-syslog-server/web"
)

func main() {
	cfgPath := os.Getenv("UNIFY_SYSLOG_CONFIG")
	if cfgPath == "" {
		cfgPath = config.DefaultPath
	}
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if (args[i] == "-c" || args[i] == "--config") && i+1 < len(args) {
			cfgPath = args[i+1]
			args = append(args[:i], args[i+2:]...)
			i--
		}
	}
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(cfgPath)
	case "user":
		err = userCmd(cfgPath, args)
	case "update":
		err = updateCmd(cfgPath, args)
	case "simulate":
		err = simulateCmd(args)
	case "version", "--version", "-v":
		fmt.Printf("unify-syslog %s (commit %s, built %s)\n", version.Version, version.Commit, version.BuildDate)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func serve(cfgPath string) error {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	log.Info("starting", "version", version.Version, "config", cfgPath)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	restart := func() {
		log.Info("restart requested")
		stop()
		go func() {
			time.Sleep(10 * time.Second)
			os.Exit(0)
		}()
	}

	// A freshly installed update that keeps failing is rolled back before it
	// touches the settings database.
	upd := newUpdater(cfg)
	if err := upd.StartupCheck(ctx); err != nil {
		if errors.Is(err, update.ErrRolledBack) {
			log.Error("rolled back", "err", err)
			return err
		}
		log.Error("update startup check", "err", err)
	}
	var ready atomic.Bool
	go upd.WatchStartup(ctx, ready.Load, restart)

	meta, err := store.OpenMeta(cfg.MetaDBPath(), cfg.SecretKeyPath())
	if err != nil {
		return fmt.Errorf("metadata db: %w", err)
	}
	upd.Snapshot = meta.Snapshot
	go upd.ImportPrevious(ctx)
	gen := meta.General()

	var logs *store.Logs
	for attempt := 1; ; attempt++ {
		logs, err = store.OpenLogs(ctx, cfg.ClickHouse, gen.RetentionDays)
		if err == nil {
			break
		}
		if attempt >= 30 || ctx.Err() != nil {
			return fmt.Errorf("clickhouse: %w", err)
		}
		log.Warn("waiting for clickhouse", "err", err)
		time.Sleep(2 * time.Second)
	}
	logs.InitRetention(gen.RetentionDays, gen.CategoryRetention, meta.ColdStorage())

	var loc atomic.Pointer[time.Location]
	setLoc := func(tz string) {
		l, err := time.LoadLocation(tz)
		if err != nil {
			l = time.Local
		}
		loc.Store(l)
	}
	setLoc(gen.Timezone)
	getLoc := func() *time.Location { return loc.Load() }

	queue := make(chan syslogd.Message, cfg.Ingest.QueueSize)
	sysStats := &syslogd.Stats{}
	ingStats := &ingest.Stats{}
	filter := &syslogd.Filter{}
	if err := api.ApplyAllowlist(filter, meta.Allowlist()); err != nil {
		log.Warn("source allowlist", "err", err)
	}
	srv := &syslogd.Server{UDPAddr: cfg.Syslog.UDP, TCPAddr: cfg.Syslog.TCP, ReadBuf: cfg.Syslog.UDPReadBuffer, Out: queue, Stats: sysStats, Log: log, Filter: filter}
	pipe := &ingest.Pipeline{In: queue, Meta: meta, Logs: logs, Workers: cfg.Ingest.Workers, BatchSize: cfg.Ingest.BatchSize,
		Flush: time.Duration(cfg.Ingest.FlushInterval) * time.Millisecond, Location: getLoc, Stats: ingStats, Log: log}

	geo := &geoip.Manager{Dir: filepath.Join(cfg.DataDir, "geoip"), Log: log, Client: &http.Client{Timeout: 10 * time.Minute}, Settings: func() geoip.Settings {
		g := meta.GeoIP()
		return geoip.Settings{Enabled: g.Enabled, AutoUpdate: g.AutoUpdate, Source: geoip.Source{Provider: g.Provider, AccountID: g.AccountID, LicenseKey: g.LicenseKey}}
	}}
	enricher := enrich.New(meta, geo)
	if err := enricher.Load(); err != nil {
		log.Warn("load client names", "err", err)
	}
	syncer := &enrich.Syncer{Enricher: enricher, Log: log}
	pipe.Enrich = enricher
	fwd := forward.NewManager(meta, filepath.Join(cfg.DataDir, "forward"), log)
	pipe.Forward = fwd.Submit

	mailer := &mail.Mailer{Meta: meta}
	notifier := &notify.Dispatcher{Meta: meta, Log: log, Loc: getLoc}
	// The web port also speaks HTTPS, for remote probes (and browsers).
	cert, err := tlscert.LoadOrCreate(filepath.Join(cfg.DataDir, "tls"), cfg.TLSCert, cfg.TLSKey)
	if err != nil {
		return fmt.Errorf("tls certificate: %w", err)
	}
	pin := tlscert.LeafPin(cert)
	tlsDir := filepath.Join(cfg.DataDir, "tls")
	if cfg.TLSCert != "" {
		tlsDir = "" // a configured certificate is not shared between nodes
	}
	haNode := &ha.Node{Cfg: cfg, Meta: meta, Log: log, Restart: restart, Pin: pin, TLSDir: tlsDir}
	hub := &probehub.Hub{Meta: meta, Out: queue, Dir: filepath.Join(cfg.DataDir, "probes"), Log: log, Mailer: mailer, Notify: notifier, Pin: pin}
	bk := &backup.Manager{Meta: meta, Logs: logs, Mailer: mailer, Notify: notifier, Dir: cfg.BackupDir(), KeyPath: cfg.SecretKeyPath(), Log: log, Restart: restart}
	al := &alerts.Engine{Meta: meta, Logs: logs, Mailer: mailer, Log: log, Started: time.Now(), Controller: enricher.Device, Forward: fwd, Notify: notifier, Loc: getLoc}
	go func() {
		sctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := meta.SeedDevices(sctx, logs); err != nil {
			log.Warn("seed device inventory", "err", err)
		}
	}()
	mon := &sysstatus.Monitor{Sys: sysStats, Ing: ingStats, Filter: filter, Logs: logs, Notify: notifier, Mailer: mailer, Log: log, DataDir: cfg.DataDir,
		Queue: func() (int, int) { return len(queue), cap(queue) }, Started: time.Now()}
	apiSrv := &api.Server{Cfg: cfg, Meta: meta, Logs: logs, Mailer: mailer, Backup: bk, Updater: upd, Entra: &auth.Entra{Meta: meta},
		Alerts: al, Notify: notifier, Forward: fwd, HA: haNode, Probes: hub, TLSPin: pin, Pipeline: pipe, Enrich: enricher, Syncer: syncer, Geo: geo, SysStats: sysStats, Status: mon, IngStats: ingStats, UI: web.Dist(), Log: log, Started: time.Now(), Restart: restart, Filter: filter,
		OnSettings: func(g store.GeneralSettings) { setLoc(g.Timezone) }}

	httpSrv := &http.Server{Addr: cfg.HTTPListen, Handler: apiSrv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 2)
	go func() { errc <- srv.Run(ctx) }()
	pipeDone := make(chan struct{})
	go func() { pipe.Run(ctx); fwd.InputDone(); close(pipeDone) }()
	// With high availability only the active node runs the scheduled jobs;
	// without it they always run.
	go haNode.Run(ctx)
	go haNode.Gate(ctx, "backups", func(ctx context.Context) { bk.Scheduler(ctx, getLoc) })
	go haNode.Gate(ctx, "alerts", al.Run)
	go mon.Run(ctx)
	go notifier.Run(ctx)
	fwdDone := make(chan struct{})
	go func() { fwd.Run(ctx); close(fwdDone) }()
	go geo.Run(ctx)
	go haNode.Gate(ctx, "probe checks", hub.Run)
	go haNode.Gate(ctx, "unifi sync", syncer.Run)
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				meta.PurgeSessions()
				meta.PurgeAudit(meta.Security().AuditRetentionDays)
			}
		}
	}()
	ln, err := net.Listen("tcp", cfg.HTTPListen)
	if err != nil {
		return fmt.Errorf("web interface: %w", err)
	}
	plainLn, tlsLn := tlsmux.Split(ln)
	ready.Store(true)
	go func() {
		log.Info("web interface listening", "addr", cfg.HTTPListen, "https", true, "pin", pin)
		if err := httpSrv.Serve(plainLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			ready.Store(false)
			errc <- err
		}
	}()
	go func() {
		tl := tls.NewListener(tlsLn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}})
		if err := httpSrv.Serve(tl); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Warn("https", "err", err)
		}
	}()

	select {
	case <-ctx.Done():
	case err = <-errc:
		if err != nil {
			log.Error("fatal", "err", err)
		}
		stop()
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpSrv.Shutdown(sctx)
	select {
	case <-pipeDone:
	case <-sctx.Done():
	}
	select {
	case <-fwdDone:
	case <-sctx.Done():
	}
	log.Info("stopped")
	return err
}

func userCmd(cfgPath string, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: user add <name> [--admin] [--password X] | user passwd <name> [--password X]")
	}
	fs := flag.NewFlagSet("user", flag.ContinueOnError)
	admin := fs.Bool("admin", false, "grant admin role")
	pw := fs.String("password", "", "password (default: read from stdin, or generate when stdin is empty)")
	sub, name := args[0], args[1]
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	meta, err := store.OpenMeta(cfg.MetaDBPath(), cfg.SecretKeyPath())
	if err != nil {
		return err
	}
	password := *pw
	generated := false
	if password == "" {
		if st, _ := os.Stdin.Stat(); st.Mode()&os.ModeCharDevice == 0 {
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			password = strings.TrimSpace(line)
		}
		if password == "" {
			password = store.RandomToken(8)
			generated = true
		}
	}
	switch sub {
	case "add":
		role := "viewer"
		if *admin {
			role = "admin"
		}
		u := &store.User{Username: name, Role: role, Provider: "local"}
		if err := meta.CreateUser(u, password); err != nil {
			return err
		}
	case "passwd":
		u, err := meta.UserByName(name)
		if err != nil {
			return fmt.Errorf("user %s not found", name)
		}
		if err := meta.SetPassword(u.ID, password); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown user command %q", sub)
	}
	if generated {
		fmt.Println(password)
	}
	return nil
}

func newUpdater(cfg *config.Config) *update.Updater {
	u := update.New()
	u.HistoryDir = filepath.Join(cfg.DataDir, "versions")
	u.MetaPath = cfg.MetaDBPath()
	return u
}

func updateCmd(cfgPath string, args []string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	meta, err := store.OpenMeta(cfg.MetaDBPath(), cfg.SecretKeyPath())
	if err != nil {
		return err
	}
	u := newUpdater(cfg)
	u.Snapshot = meta.Snapshot
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if len(args) > 0 && args[0] == "rollback" {
		target := ""
		if len(args) > 1 {
			target = args[1]
		}
		ver, err := u.Rollback(ctx, target, nil)
		if err != nil {
			return err
		}
		fmt.Printf("rolled back to %s; restart the service: systemctl restart unify-syslog\n", ver)
		return nil
	}
	channel := meta.Update().Channel
	res, err := u.Check(ctx, channel)
	if err != nil {
		return err
	}
	fmt.Printf("current %s, latest %s (%s channel)\n", res.Current, res.Latest, res.Channel)
	if len(args) > 0 && args[0] == "--check" {
		return nil
	}
	if !res.Available {
		fmt.Println("already up to date")
		return nil
	}
	if err := u.Apply(ctx, channel, nil); err != nil {
		return err
	}
	fmt.Println("installed; restart the service: systemctl restart unify-syslog")
	return nil
}

func simulateCmd(args []string) error {
	fs := flag.NewFlagSet("simulate", flag.ContinueOnError)
	target := fs.String("target", "127.0.0.1:514", "syslog server address (UDP)")
	rate := fs.Int("rate", 200, "messages per second")
	dur := fs.Duration("duration", time.Minute, "how long to send")
	sites := fs.Int("sites", 3, "number of simulated sites")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return simulate.Run(*target, *rate, *dur, *sites, func(s string) { fmt.Println(s) })
}
