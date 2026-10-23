package store

import (
	"context"
	"time"
)

// LogHealth is the state of the ClickHouse log store.
type LogHealth struct {
	OK        bool    `json:"ok"`
	Error     string  `json:"error,omitempty"`
	LatencyMS float64 `json:"latency_ms"`
	Version   string  `json:"version"`
	UptimeSec uint64  `json:"uptime_sec"`
	// Parts are the active data parts of the logs table; many parts in one
	// partition mean merges cannot keep up with inserts.
	Parts         uint64 `json:"parts"`
	MaxPartsInDay uint64 `json:"max_parts_per_partition"`
	Merges        uint64 `json:"merges"`
	Mutations     uint64 `json:"mutations"`
	MemoryBytes   int64  `json:"memory_bytes"`
	Disks         []Disk `json:"disks"`
}

// Disk is a filesystem and how full it is.
type Disk struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

// Health checks ClickHouse; it never fails, problems are in OK and Error.
func (l *Logs) Health(ctx context.Context) LogHealth {
	h := LogHealth{Disks: []Disk{}}
	start := time.Now()
	if err := l.Conn.Ping(ctx); err != nil {
		h.Error = err.Error()
		return h
	}
	h.LatencyMS = float64(time.Since(start).Microseconds()) / 1000
	h.OK = true
	l.Conn.QueryRow(ctx, "SELECT version(), toUInt64(uptime())").Scan(&h.Version, &h.UptimeSec)
	l.Conn.QueryRow(ctx, `SELECT toUInt64(sum(n)), toUInt64(max(n)) FROM (SELECT partition, count() AS n FROM `+l.system("parts")+` WHERE active AND database = ? AND table = ? GROUP BY partition)`, l.db, l.localName("logs")).
		Scan(&h.Parts, &h.MaxPartsInDay)
	l.Conn.QueryRow(ctx, `SELECT count() FROM system.merges WHERE database = ?`, l.db).Scan(&h.Merges)
	l.Conn.QueryRow(ctx, `SELECT count() FROM system.mutations WHERE database = ? AND NOT is_done`, l.db).Scan(&h.Mutations)
	l.Conn.QueryRow(ctx, `SELECT toInt64(value) FROM system.metrics WHERE metric = 'MemoryTracking'`).Scan(&h.MemoryBytes)
	if rows, err := l.Conn.Query(ctx, `SELECT name, path, total_space, free_space FROM system.disks WHERE total_space < 9223372036854775807`); err == nil {
		for rows.Next() {
			var d Disk
			if rows.Scan(&d.Name, &d.Path, &d.Total, &d.Free) == nil {
				h.Disks = append(h.Disks, d)
			}
		}
		rows.Close()
	}
	return h
}
