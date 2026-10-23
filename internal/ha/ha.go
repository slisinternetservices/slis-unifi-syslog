// Package ha runs two servers as an active/standby pair. keepalived moves a
// virtual IP between them; both store logs in the same external ClickHouse.
// The node holding the virtual IP is active: it runs the scheduled jobs
// (alert rules, backups, probe and device checks, UniFi sync). The standby
// receives syslog only when the virtual IP moves to it, and copies the
// settings database, secret key and TLS certificate of the active node
// whenever the settings there change.
package ha

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/config"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/tlscert"
	"github.com/rhisijmuiden/unify-syslog-server/internal/version"
)

// Node is this server's side of the pair.
type Node struct {
	Cfg  *config.Config
	Meta *store.Meta
	Log  *slog.Logger
	// Restart restarts the process, to load a copied database.
	Restart func()
	// Pin is the public key pin of this server's TLS certificate.
	Pin string
	// TLSDir holds the self-signed certificate, which both nodes share so
	// probes trust either; empty when a configured certificate is used.
	TLSDir string
	// LocalAddrs lists the addresses of this machine (tests replace it).
	LocalAddrs func() ([]netip.Addr, error)

	mu      sync.Mutex
	cfg     config.HA
	active  bool
	since   time.Time
	changed chan struct{}
	peer    PeerState
	sync    SyncState
	syncing bool
}

// PeerState is what the last status call to the peer returned.
type PeerState struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Active      bool   `json:"active"`
	Version     string `json:"version"`
	Fingerprint string `json:"fingerprint"`
	Seen        int64  `json:"seen"`
	Error       string `json:"error"`
}

type SyncState struct {
	Last    int64  `json:"last"`
	Error   string `json:"error"`
	Pending bool   `json:"pending"`
}

// State is shown on the High availability page.
type State struct {
	Enabled  bool      `json:"enabled"`
	Active   bool      `json:"active"`
	Since    int64     `json:"since"`
	NodeName string    `json:"node_name"`
	VIP      string    `json:"vip"`
	SelfURL  string    `json:"self_url"`
	Peer     PeerState `json:"peer"`
	Sync     SyncState `json:"sync"`
	LastSync int64     `json:"last_sync"`
	Version  string    `json:"version"`
	// Config is this node's setup (the secret is never included).
	Config config.HA `json:"config"`
}

// Status is what a node tells its peer.
type Status struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Active      bool   `json:"active"`
	Version     string `json:"version"`
	Fingerprint string `json:"fingerprint"`
}

func (n *Node) init() {
	if n.changed == nil {
		n.changed = make(chan struct{})
		n.cfg = n.Cfg.HA
		n.active = !n.cfg.Enabled
		n.since = time.Now()
		if n.LocalAddrs == nil {
			n.LocalAddrs = localAddrs
		}
	}
}

// Enabled tells whether this node is part of a pair.
func (n *Node) Enabled() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.init()
	return n.cfg.Enabled
}

// Active tells whether this node runs the scheduled jobs: it holds the
// virtual IP, or high availability is off.
func (n *Node) Active() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.init()
	return n.active
}

// Standby tells whether this node follows the other one; changes made here
// would be overwritten.
func (n *Node) Standby() bool { return n.Enabled() && !n.Active() }

func (n *Node) State() State {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.init()
	c := n.cfg
	c.Secret = ""
	return State{Enabled: c.Enabled, Active: n.active, Since: n.since.Unix(), NodeName: c.NodeName, VIP: c.VIP, SelfURL: c.SelfURL,
		Peer: n.peer, Sync: n.sync, LastSync: n.sync.Last, Version: version.Version, Config: c}
}

// PeerURL is where the other node is reached ("" without a pair).
func (n *Node) PeerURL() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.init()
	return n.cfg.PeerURL
}

func (n *Node) setActive(a bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if a == n.active {
		return
	}
	n.active, n.since = a, time.Now()
	close(n.changed)
	n.changed = make(chan struct{})
	role := "standby"
	if a {
		role = "active"
	}
	n.Log.Info("high availability: role changed", "role", role)
}

// watch returns the current role and a channel closed when it changes.
func (n *Node) watch() (bool, <-chan struct{}) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.init()
	return n.active, n.changed
}

// Gate runs job while this node is active and stops it (cancelling its
// context and waiting for it) when the node becomes standby.
func (n *Node) Gate(ctx context.Context, name string, job func(context.Context)) {
	for {
		active, changed := n.watch()
		if !active {
			select {
			case <-ctx.Done():
				return
			case <-changed:
				continue
			}
		}
		jctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { job(jctx); close(done) }()
		select {
		case <-ctx.Done():
			cancel()
			<-done
			return
		case <-changed:
			cancel()
			<-done
			if ctx.Err() == nil {
				n.Log.Info("high availability: job paused or resumed", "job", name)
			}
		case <-done:
			cancel()
			return
		}
	}
}

func localAddrs() ([]netip.Addr, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	out := []netip.Addr{}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(ipn.IP); ok {
				out = append(out, ip.Unmap())
			}
		}
	}
	return out, nil
}

// VIPAddr is the address part of a virtual IP such as 192.168.1.50/24.
func VIPAddr(vip string) (netip.Addr, error) {
	if p, err := netip.ParsePrefix(vip); err == nil {
		return p.Addr(), nil
	}
	return netip.ParseAddr(vip)
}

// holdsVIP tells whether the virtual IP is on one of this machine's
// interfaces.
func (n *Node) holdsVIP(vip string) bool {
	a, err := VIPAddr(vip)
	if err != nil {
		return false
	}
	addrs, err := n.LocalAddrs()
	if err != nil {
		return false
	}
	for _, l := range addrs {
		if l == a {
			return true
		}
	}
	return false
}

// Run follows the virtual IP and keeps a standby in sync.
func (n *Node) Run(ctx context.Context) {
	n.mu.Lock()
	n.init()
	n.mu.Unlock()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	var lastPoll time.Time
	for {
		n.mu.Lock()
		c := n.cfg
		n.mu.Unlock()
		if c.Enabled {
			n.setActive(n.holdsVIP(c.VIP))
			every := time.Duration(max(c.SyncSeconds, 10)) * time.Second
			if c.PeerURL != "" && time.Since(lastPoll) >= every {
				lastPoll = time.Now()
				n.poll(ctx, c)
			}
		} else {
			n.setActive(true)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// poll asks the peer for its state, and a standby copies the settings of
// an active peer when they differ from its own.
func (n *Node) poll(ctx context.Context, c config.HA) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var st Status
	err := n.call(ctx, c, http.MethodGet, "/api/ha/v1/status", nil, &st)
	n.mu.Lock()
	if err != nil {
		n.peer.Error = err.Error()
	} else {
		n.peer = PeerState{Name: st.Name, URL: c.PeerURL, Active: st.Active, Version: st.Version, Fingerprint: st.Fingerprint, Seen: time.Now().Unix()}
	}
	active, syncing := n.active, n.syncing
	n.mu.Unlock()
	if err != nil || active || !st.Active || syncing {
		return
	}
	own, err := n.Meta.ConfigFingerprint()
	if err != nil || own == st.Fingerprint {
		n.mu.Lock()
		n.sync.Pending = false
		if err == nil && n.sync.Last == 0 {
			// In sync since start-up (a restart after the last copy).
			n.sync.Last = time.Now().Unix()
		}
		n.mu.Unlock()
		return
	}
	n.mu.Lock()
	n.sync.Pending = true
	n.mu.Unlock()
	if err := n.SyncNow(ctx); err != nil {
		n.Log.Warn("high availability: copying settings from the active node failed", "err", err)
	}
}

// SyncNow copies the database, secret key and certificate of the active
// peer and restarts to load them.
func (n *Node) SyncNow(ctx context.Context) error {
	n.mu.Lock()
	if n.syncing {
		n.mu.Unlock()
		return errors.New("already copying")
	}
	n.syncing = true
	c := n.cfg
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		n.syncing = false
		n.mu.Unlock()
	}()
	err := n.fetchSnapshot(ctx, c)
	n.mu.Lock()
	if err != nil {
		n.sync.Error = err.Error()
	} else {
		n.sync = SyncState{Last: time.Now().Unix()}
	}
	n.mu.Unlock()
	if err != nil {
		return err
	}
	n.Log.Info("high availability: copied settings from the active node, restarting")
	if n.Restart != nil {
		n.Restart()
	}
	return nil
}

func (n *Node) fetchSnapshot(ctx context.Context, c config.HA) error {
	req, err := n.request(ctx, c, http.MethodGet, "/api/ha/v1/snapshot", nil)
	if err != nil {
		return err
	}
	resp, err := n.client(c).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return httpError(resp)
	}
	tr := tar.NewReader(io.LimitReader(resp.Body, 4<<30))
	files := map[string]string{
		"meta.db":    n.Cfg.MetaDBPath() + ".restore",
		"secret.key": n.Cfg.SecretKeyPath(),
	}
	if n.TLSDir != "" {
		files["cert.pem"] = filepath.Join(n.TLSDir, "cert.pem")
		files["key.pem"] = filepath.Join(n.TLSDir, "key.pem")
	}
	staged := map[string]string{}
	defer func() {
		for _, tmp := range staged {
			os.Remove(tmp)
		}
	}()
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		dst, ok := files[h.Name]
		if !ok {
			continue
		}
		tmp := dst + ".ha"
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, tr)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		staged[dst] = tmp
	}
	if _, ok := staged[files["meta.db"]]; !ok {
		return errors.New("the active node sent no database")
	}
	// Everything arrived: put it in place. The database is applied at the
	// next start (meta.db.restore).
	for dst, tmp := range staged {
		if err := os.Rename(tmp, dst); err != nil {
			return err
		}
		delete(staged, dst)
	}
	if p := files["cert.pem"]; p != "" {
		os.Chmod(p, 0o644)
	}
	return nil
}

// WriteSnapshot streams what a standby needs: the settings database, the
// secret key and the self-signed certificate.
func (n *Node) WriteSnapshot(w io.Writer) error {
	dir, err := os.MkdirTemp(n.Cfg.DataDir, ".ha-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db := filepath.Join(dir, "meta.db")
	if err := n.Meta.Snapshot(db); err != nil {
		return err
	}
	files := [][2]string{{"meta.db", db}, {"secret.key", n.Cfg.SecretKeyPath()}}
	if n.TLSDir != "" {
		files = append(files, [2]string{"cert.pem", filepath.Join(n.TLSDir, "cert.pem")}, [2]string{"key.pem", filepath.Join(n.TLSDir, "key.pem")})
	}
	tw := tar.NewWriter(w)
	for _, f := range files {
		b, err := os.ReadFile(f[1])
		if err != nil {
			if os.IsNotExist(err) && f[0] != "meta.db" {
				continue
			}
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: f[0], Mode: 0o600, Size: int64(len(b)), ModTime: time.Now()}); err != nil {
			return err
		}
		if _, err := tw.Write(b); err != nil {
			return err
		}
	}
	return tw.Close()
}

// LocalStatus is this node's answer to the peer.
func (n *Node) LocalStatus() Status {
	fp, _ := n.Meta.ConfigFingerprint()
	n.mu.Lock()
	defer n.mu.Unlock()
	n.init()
	return Status{Name: n.cfg.NodeName, URL: n.cfg.SelfURL, Active: n.active, Version: version.Version, Fingerprint: fp}
}

// ---------------------------------------------------------------- peer authentication

// Authorized checks the shared secret of a call from the peer.
func (n *Node) Authorized(r *http.Request) bool {
	n.mu.Lock()
	secret := n.cfg.Secret
	n.mu.Unlock()
	got := r.Header.Get("X-USS-HA")
	if secret == "" || got == "" {
		return false
	}
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(secret))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func (n *Node) request(ctx context.Context, c config.HA, method, path string, body any) (*http.Request, error) {
	if c.PeerURL == "" {
		return nil, errors.New("no peer configured")
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.PeerURL, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-USS-HA", c.Secret)
	req.Header.Set("X-USS", "1")
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// client trusts the peer's pinned certificate, or this node's own (after
// the first copy both nodes share one certificate).
func (n *Node) client(c config.HA) *http.Client {
	pins := []string{c.PeerPin, n.Pin}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("peer sent no certificate")
			}
			got := tlscert.Pin(cs.PeerCertificates[0])
			for _, p := range pins {
				if p != "" && p == got {
					return nil
				}
			}
			inter := x509.NewCertPool()
			for _, x := range cs.PeerCertificates[1:] {
				inter.AddCert(x)
			}
			if _, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{DNSName: cs.ServerName, Intermediates: inter}); err != nil {
				return errors.New("the peer's certificate does not match the paired node")
			}
			return nil
		}}
	tr := &http.Transport{TLSClientConfig: tc, Proxy: nil, ResponseHeaderTimeout: 30 * time.Second}
	return &http.Client{Transport: tr, Timeout: 10 * time.Minute}
}

func (n *Node) call(ctx context.Context, c config.HA, method, path string, body, out any) error {
	req, err := n.request(ctx, c, method, path, body)
	if err != nil {
		return err
	}
	resp, err := n.client(c).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return httpError(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func httpError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return errors.New(e.Error)
	}
	return fmt.Errorf("peer answered %s", resp.Status)
}

// ---------------------------------------------------------------- setup

// Setup is what the interface sends to set up this node.
type Setup struct {
	NodeName  string `json:"node_name"`
	SelfURL   string `json:"self_url"`
	VIP       string `json:"vip"`
	Interface string `json:"interface"`
	Priority  int    `json:"priority"`
	// Code joins an existing pair (from the first node); empty starts one.
	Code string `json:"code"`
}

// pairCode is what the first node hands to the second.
type pairCode struct {
	URL      string `json:"u"`
	Pin      string `json:"p"`
	Secret   string `json:"s"`
	VIP      string `json:"v"`
	RouterID int    `json:"r"`
}

func validURL(u string) error {
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "https" || p.Host == "" {
		return errors.New("the node URL must look like https://192.168.1.10:8080")
	}
	return nil
}

func randInt(n int64) int {
	v, _ := rand.Int(rand.Reader, big.NewInt(n))
	return int(v.Int64())
}

func (s *Setup) clean() error {
	s.NodeName = strings.TrimSpace(s.NodeName)
	if s.NodeName == "" {
		h, _ := os.Hostname()
		s.NodeName = h
	}
	s.SelfURL = strings.TrimRight(strings.TrimSpace(s.SelfURL), "/")
	if err := validURL(s.SelfURL); err != nil {
		return err
	}
	if s.Priority < 1 || s.Priority > 254 {
		s.Priority = 100
	}
	if s.Interface == "" {
		s.Interface = "eth0"
	}
	return nil
}

// Start makes this node the first of a pair.
func (n *Node) Start(s Setup) error {
	if err := s.clean(); err != nil {
		return err
	}
	if _, err := netip.ParsePrefix(s.VIP); err != nil {
		return errors.New("the virtual IP must include the prefix length, e.g. 192.168.1.50/24")
	}
	sec := make([]byte, 24)
	rand.Read(sec)
	c := config.HA{Enabled: true, VIP: s.VIP, NodeName: s.NodeName, SelfURL: s.SelfURL, Secret: hex.EncodeToString(sec),
		SyncSeconds: 30, Interface: s.Interface, Priority: s.Priority, RouterID: 1 + randInt(254)}
	return n.save(c, nil)
}

// Code is the pairing code for the second node.
func (n *Node) Code() (string, error) {
	n.mu.Lock()
	c := n.cfg
	n.mu.Unlock()
	if !c.Enabled || c.Secret == "" {
		return "", errors.New("high availability is not set up")
	}
	b, _ := json.Marshal(pairCode{URL: c.SelfURL, Pin: n.Pin, Secret: c.Secret, VIP: c.VIP, RouterID: c.RouterID})
	return "USSHA1." + base64.RawURLEncoding.EncodeToString(b), nil
}

// JoinRequest is what the second node sends to the first.
type JoinRequest struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Pin  string `json:"pin"`
}

// JoinResponse hands the second node what it shares with the first.
type JoinResponse struct {
	Name       string            `json:"name"`
	ClickHouse config.ClickHouse `json:"clickhouse"`
}

// Join pairs this node with the first one and takes over its ClickHouse
// connection. The caller restarts afterwards.
func (n *Node) Join(ctx context.Context, s Setup) error {
	if err := s.clean(); err != nil {
		return err
	}
	raw, ok := strings.CutPrefix(strings.TrimSpace(s.Code), "USSHA1.")
	if !ok {
		return errors.New("this is not a pairing code")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	var pc pairCode
	if err != nil || json.Unmarshal(b, &pc) != nil || pc.Secret == "" {
		return errors.New("this is not a pairing code")
	}
	c := config.HA{Enabled: true, VIP: pc.VIP, NodeName: s.NodeName, SelfURL: s.SelfURL, PeerURL: pc.URL, PeerPin: pc.Pin, Secret: pc.Secret,
		SyncSeconds: 30, Interface: s.Interface, Priority: s.Priority, RouterID: pc.RouterID}
	var resp JoinResponse
	if err := n.call(ctx, c, http.MethodPost, "/api/ha/v1/join", JoinRequest{Name: s.NodeName, URL: s.SelfURL, Pin: n.Pin}, &resp); err != nil {
		return fmt.Errorf("contacting the first node: %w", err)
	}
	return n.save(c, &resp.ClickHouse)
}

// Accept registers the second node (on the first).
func (n *Node) Accept(j JoinRequest, ch config.ClickHouse) (JoinResponse, error) {
	if err := validURL(j.URL); err != nil {
		return JoinResponse{}, err
	}
	n.mu.Lock()
	c := n.cfg
	n.mu.Unlock()
	c.PeerURL, c.PeerPin = strings.TrimRight(j.URL, "/"), j.Pin
	if err := n.save(c, nil); err != nil {
		return JoinResponse{}, err
	}
	return JoinResponse{Name: c.NodeName, ClickHouse: ch}, nil
}

// Update changes this node's own settings (name, URL, interface, priority).
func (n *Node) Update(s Setup) error {
	if err := s.clean(); err != nil {
		return err
	}
	n.mu.Lock()
	c := n.cfg
	n.mu.Unlock()
	if !c.Enabled {
		return errors.New("high availability is not set up")
	}
	c.NodeName, c.SelfURL, c.Interface, c.Priority = s.NodeName, s.SelfURL, s.Interface, s.Priority
	return n.save(c, nil)
}

// Leave turns high availability off on this node.
func (n *Node) Leave() error { return n.save(config.HA{}, nil) }

func (n *Node) save(c config.HA, ch *config.ClickHouse) error {
	node, err := n.Cfg.LoadNode()
	if err != nil {
		return err
	}
	if c.Enabled {
		node.HA = &c
	} else {
		node.HA = nil
	}
	if ch != nil && ch.Addr != "" {
		node.ClickHouse = ch
	}
	if err := n.Cfg.SaveNode(node); err != nil {
		return err
	}
	n.mu.Lock()
	n.init()
	n.cfg = c
	n.peer = PeerState{}
	n.mu.Unlock()
	if !c.Enabled {
		n.setActive(true)
	} else {
		n.setActive(n.holdsVIP(c.VIP))
	}
	return nil
}
