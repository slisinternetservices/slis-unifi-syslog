package store

import (
	"fmt"
	"net/netip"
	"strings"
)

// MonitoringSettings control the Prometheus metrics endpoint and the
// read-only ClickHouse endpoint for Grafana.
type MonitoringSettings struct {
	// MetricsEnabled serves /metrics. Scrapers authenticate with an API
	// token, unless they connect from one of AnonymousCIDRs.
	MetricsEnabled bool     `json:"metrics_enabled"`
	AnonymousCIDRs []string `json:"anonymous_cidrs"`
	// GrafanaEnabled serves /clickhouse: the ClickHouse HTTP interface,
	// read-only, for the Grafana ClickHouse data source.
	GrafanaEnabled bool `json:"grafana_enabled"`
}

func (m *Meta) Monitoring() MonitoringSettings {
	s := MonitoringSettings{MetricsEnabled: true}
	m.GetSetting("monitoring", &s)
	if s.AnonymousCIDRs == nil {
		s.AnonymousCIDRs = []string{}
	}
	return s
}

// CleanCIDRs validates and normalises networks such as 10.0.0.0/8; a bare
// address becomes a /32 or /128.
func CleanCIDRs(in []string) ([]string, error) {
	out := []string{}
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			a, err := netip.ParseAddr(c)
			if err != nil {
				return nil, fmt.Errorf("invalid address %q", c)
			}
			out = append(out, netip.PrefixFrom(a, a.BitLen()).String())
			continue
		}
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, fmt.Errorf("invalid network %q", c)
		}
		out = append(out, p.Masked().String())
	}
	return out, nil
}

// InCIDRs tells whether ip lies in one of the networks.
func InCIDRs(ip string, cidrs []string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, c := range cidrs {
		if p, err := netip.ParsePrefix(c); err == nil && p.Contains(a) {
			return true
		}
	}
	return false
}
