package syslogd

import (
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Filter is the source allowlist: when enabled, only senders inside one of
// the networks are accepted. Rejected senders are remembered so they can be
// shown (and allowed with one click) in the interface.
type Filter struct {
	nets     atomic.Pointer[[]netip.Prefix] // nil = accept everyone
	Rejected atomic.Uint64

	mu     sync.Mutex
	recent map[string]*RejectedSender
}

type RejectedSender struct {
	IP       string `json:"ip"`
	Count    uint64 `json:"count"`
	First    int64  `json:"first"`
	Last     int64  `json:"last"`
	Protocol string `json:"protocol"`
}

const maxRecent = 200

// Set replaces the allowlist; enabled=false accepts every sender.
func (f *Filter) Set(enabled bool, nets []netip.Prefix) {
	if !enabled {
		f.nets.Store(nil)
		return
	}
	n := append([]netip.Prefix(nil), nets...)
	f.nets.Store(&n)
}

func (f *Filter) Enabled() bool { return f != nil && f.nets.Load() != nil }

// Allow reports whether ip may send logs, recording a rejection if not.
func (f *Filter) Allow(ip, proto string) bool {
	if f == nil {
		return true
	}
	nets := f.nets.Load()
	if nets == nil {
		return true
	}
	if a, err := netip.ParseAddr(ip); err == nil {
		a = a.Unmap()
		for _, p := range *nets {
			if p.Contains(a) {
				return true
			}
		}
	}
	f.Rejected.Add(1)
	f.remember(ip, proto)
	return false
}

func (f *Filter) remember(ip, proto string) {
	now := time.Now().Unix()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recent == nil {
		f.recent = map[string]*RejectedSender{}
	}
	if r := f.recent[ip]; r != nil {
		r.Count++
		r.Last = now
		r.Protocol = proto
		return
	}
	if len(f.recent) >= maxRecent {
		var oldest string
		var at int64
		for k, r := range f.recent {
			if oldest == "" || r.Last < at {
				oldest, at = k, r.Last
			}
		}
		delete(f.recent, oldest)
	}
	f.recent[ip] = &RejectedSender{IP: ip, Count: 1, First: now, Last: now, Protocol: proto}
}

// RecentRejected lists rejected senders, most recent first.
func (f *Filter) RecentRejected() []RejectedSender {
	f.mu.Lock()
	out := make([]RejectedSender, 0, len(f.recent))
	for _, r := range f.recent {
		out = append(out, *r)
	}
	f.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Last > out[j].Last })
	return out
}

// Forget drops remembered rejections for addresses that are now allowed.
func (f *Filter) Forget() {
	f.mu.Lock()
	defer f.mu.Unlock()
	nets := f.nets.Load()
	for ip := range f.recent {
		a, err := netip.ParseAddr(ip)
		if nets == nil || err != nil {
			delete(f.recent, ip)
			continue
		}
		for _, p := range *nets {
			if p.Contains(a.Unmap()) {
				delete(f.recent, ip)
				break
			}
		}
	}
}
