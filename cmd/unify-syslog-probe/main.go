// Command unify-syslog-probe is the remote probe of UniFi Syslog Server: it
// receives syslog at a site, buffers it on disk and forwards it over HTTPS
// to the main server.
//
//	unify-syslog-probe [run]              run the probe (the systemd service)
//	unify-syslog-probe enroll <key>       connect to a main server with an enrollment key
//	unify-syslog-probe setup              ask for the enrollment key and enroll
//	unify-syslog-probe status             show the probe's status
//	unify-syslog-probe reset              forget the enrollment (the buffer is kept)
//	unify-syslog-probe install-files [--root /]   write the systemd unit, service user and directories
//	unify-syslog-probe version
//
// Options: --data-dir (default /var/lib/unify-syslog-probe, or
// $UNIFY_PROBE_DATA) and --ui (local page, default :8080, or $UNIFY_PROBE_UI).
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/probe"
	"github.com/rhisijmuiden/unify-syslog-server/internal/version"
)

func main() {
	dataDir := envOr("UNIFY_PROBE_DATA", probe.DefaultDataDir)
	uiAddr := envOr("UNIFY_PROBE_UI", probe.DefaultUIAddr)
	var args []string
	for i := 1; i < len(os.Args); i++ {
		a := os.Args[i]
		switch {
		case (a == "--data-dir" || a == "--ui") && i+1 < len(os.Args):
			if a == "--ui" {
				uiAddr = os.Args[i+1]
			} else {
				dataDir = os.Args[i+1]
			}
			i++
		case strings.HasPrefix(a, "--data-dir="):
			dataDir = strings.TrimPrefix(a, "--data-dir=")
		case strings.HasPrefix(a, "--ui="):
			uiAddr = strings.TrimPrefix(a, "--ui=")
		default:
			args = append(args, a)
		}
	}
	cmd := "run"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "run", "serve":
		err = run(dataDir, uiAddr)
	case "enroll":
		if len(args) < 1 {
			err = errors.New("usage: unify-syslog-probe enroll <enrollment key>")
			break
		}
		err = enroll(dataDir, uiAddr, args[0])
	case "setup":
		err = setup(dataDir, uiAddr)
	case "status":
		err = status(dataDir, uiAddr)
	case "reset":
		err = reset(dataDir)
	case "install-files":
		root := "/"
		if len(args) == 2 && args[0] == "--root" {
			root = args[1]
		}
		var files []string
		files, err = probe.InstallFiles(root)
		for _, f := range files {
			fmt.Println(f)
		}
	case "version", "--version", "-v":
		fmt.Printf("unify-syslog-probe %s (commit %s, built %s)\n", version.Version, version.Commit, version.BuildDate)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func run(dataDir, uiAddr string) error {
	level := new(slog.LevelVar)
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	if probe.StartupCheck(dataDir, log.Error) {
		return errors.New("rolled back to the previous version; restarting")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	a := &probe.Agent{DataDir: dataDir, UIAddr: uiAddr, Log: log, Level: level, Restart: stop}
	if err := a.Open(); err != nil {
		return err
	}
	go a.WatchUpdate(ctx)
	return a.Run(ctx)
}

// localURL is the running probe's own page, used so the CLI enrolls
// through the service instead of editing its files underneath it.
func localURL(uiAddr string) string {
	host, port, err := net.SplitHostPort(uiAddr)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func enroll(dataDir, uiAddr, key string) error {
	key = strings.TrimSpace(key)
	if u := localURL(uiAddr); u != "" {
		b, _ := json.Marshal(map[string]string{"key": key})
		req, _ := http.NewRequest(http.MethodPost, u+"/api/enroll", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-USS", "1")
		resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
		if err == nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			var ls probe.LocalStatus
			var e struct{ Error string }
			if resp.StatusCode != 200 {
				json.Unmarshal(body, &e)
				return errors.New(e.Error)
			}
			json.Unmarshal(body, &ls)
			fmt.Printf("Enrolled as %q (site %s) with %s\n", ls.Name, ls.Site, ls.Server)
			return nil
		}
	}
	// The service is not running: enroll directly.
	a := &probe.Agent{DataDir: dataDir, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := a.Open(); err != nil {
		return err
	}
	st, err := a.Enroll(context.Background(), key)
	if err != nil {
		return err
	}
	fmt.Printf("Enrolled as %q (site %s) with %s\nStart the probe: systemctl restart unify-syslog-probe\n", st.Name, st.Site, st.Server)
	return nil
}

func setup(dataDir, uiAddr string) error {
	fmt.Println("UniFi Syslog probe setup")
	fmt.Println("On the main server, open Probes, click Add probe and copy the enrollment key.")
	in := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("Enrollment key: ")
		line, err := in.ReadString('\n')
		key := strings.TrimSpace(line)
		if key == "" {
			if err != nil {
				return errors.New("no enrollment key entered")
			}
			continue
		}
		if err := enroll(dataDir, uiAddr, key); err != nil {
			fmt.Println("Could not enroll:", err)
			continue
		}
		return nil
	}
}

func status(dataDir, uiAddr string) error {
	u := localURL(uiAddr)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(u + "/api/status")
	if err != nil {
		return fmt.Errorf("the probe service is not running (%v)", err)
	}
	defer resp.Body.Close()
	var ls probe.LocalStatus
	if err := json.NewDecoder(resp.Body).Decode(&ls); err != nil {
		return err
	}
	s := ls.Status
	state := "not enrolled"
	switch {
	case ls.Revoked:
		state = "key revoked by the main server"
	case ls.Enrolled && ls.LastContact > 0 && time.Since(time.Unix(ls.LastContact, 0)) < 90*time.Second:
		state = "connected"
	case ls.Enrolled:
		state = "not connected"
	}
	fmt.Printf("Probe:      %s (site %s)\nState:      %s\nServer:     %s\nVersion:    %s\nListening:  %s\nReceived:   %d (%.1f/s), sent %d\nBuffered:   %d messages, %d bytes\nDevices:    %d in the last 24 hours\n",
		or(ls.Name, s.Hostname), or(ls.Site, "-"), state, or(ls.Server, "-"), ls.Version, s.Listen, s.Received, s.Rate, s.Sent, s.BufferRecords, s.BufferBytes, s.Devices)
	if ls.LastError != "" {
		fmt.Printf("Last error: %s\n", ls.LastError)
	}
	return nil
}

func reset(dataDir string) error {
	a := &probe.Agent{DataDir: dataDir, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := a.Open(); err != nil {
		return err
	}
	if err := a.Reset(); err != nil {
		return err
	}
	fmt.Println("Enrollment removed. Restart the probe and enroll it again: systemctl restart unify-syslog-probe")
	return nil
}

func or(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
