// Package diskqueue is an on-disk FIFO queue of newline-delimited records.
// Forwarding destinations and remote probes use it so that nothing queued
// is lost when the receiver is down or the process restarts.
package diskqueue

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Queue is a per-destination on-disk queue of newline-delimited records.
// Records are appended to segment files; a cursor file remembers how far
// delivery has been confirmed, so nothing that was queued is lost when the
// destination is down or the server restarts. Beyond the size limit the
// oldest segment is dropped and counted.
type Queue struct {
	dir      string
	maxBytes int64
	segMax   int64

	mu     sync.Mutex
	segs   []uint64 // existing segments, oldest first
	size   map[uint64]int64
	lines  map[uint64]int64
	w      *os.File
	cur    pos   // first unconfirmed record
	drop   int64 // records dropped because the queue was full, not yet taken
	notify chan struct{}
}

type pos struct {
	seg  uint64
	off  int64
	line int64 // records before off in seg
}

// Item is one queued record and the position just after it.
type Item struct {
	Data []byte
	end  pos
}

const defaultSegment = 16 << 20

func segName(n uint64) string { return fmt.Sprintf("%016d.seg", n) }

// Open opens or creates the queue in dir.
func Open(dir string, maxBytes int64) (*Queue, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	q := &Queue{dir: dir, maxBytes: maxBytes, segMax: defaultSegment, size: map[uint64]int64{}, lines: map[uint64]int64{}, notify: make(chan struct{}, 1)}
	if q.maxBytes > 0 && q.segMax > q.maxBytes/4 {
		q.segMax = max(q.maxBytes/4, 64<<10)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		if n, ok := strings.CutSuffix(e.Name(), ".seg"); ok {
			if v, err := strconv.ParseUint(n, 10, 64); err == nil {
				q.segs = append(q.segs, v)
			}
		}
	}
	slices.Sort(q.segs)
	for _, s := range q.segs {
		if err := q.scan(s); err != nil {
			return nil, err
		}
	}
	if len(q.segs) == 0 {
		q.segs = []uint64{1}
	}
	last := q.segs[len(q.segs)-1]
	q.w, err = os.OpenFile(filepath.Join(dir, segName(last)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, err
	}
	q.cur = pos{seg: q.segs[0]}
	if b, err := os.ReadFile(filepath.Join(dir, "cursor")); err == nil {
		var c pos
		if _, err := fmt.Sscanf(string(b), "%d %d %d", &c.seg, &c.off, &c.line); err == nil && slices.Contains(q.segs, c.seg) && c.off <= q.size[c.seg] {
			q.cur = c
		}
	}
	// Segments before the cursor were confirmed but not yet removed.
	for len(q.segs) > 1 && q.segs[0] < q.cur.seg {
		q.removeSeg(q.segs[0])
	}
	return q, nil
}

// scan counts the records of a segment and cuts off a partly written last
// line (a crash during a write).
func (q *Queue) scan(s uint64) error {
	p := filepath.Join(q.dir, segName(s))
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	var n, good, off int64
	buf := make([]byte, 256<<10)
	for {
		k, err := f.Read(buf)
		for i := 0; i < k; i++ {
			if buf[i] == '\n' {
				n++
				good = off + int64(i) + 1
			}
		}
		off += int64(k)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if good < off {
		if err := os.Truncate(p, good); err != nil {
			return err
		}
	}
	q.size[s], q.lines[s] = good, n
	return nil
}

func (q *Queue) removeSeg(s uint64) {
	os.Remove(filepath.Join(q.dir, segName(s)))
	q.segs = slices.DeleteFunc(q.segs, func(v uint64) bool { return v == s })
	delete(q.size, s)
	delete(q.lines, s)
}

// Append queues records; each must be a single line without "\n".
func (q *Queue) Append(recs [][]byte) error {
	if len(recs) == 0 {
		return nil
	}
	var b bytes.Buffer
	for _, r := range recs {
		b.Write(r)
		b.WriteByte('\n')
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	last := q.segs[len(q.segs)-1]
	if q.size[last] > 0 && q.size[last]+int64(b.Len()) > q.segMax {
		f, err := os.OpenFile(filepath.Join(q.dir, segName(last+1)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
		if err != nil {
			return err
		}
		q.w.Close()
		q.w = f
		last++
		q.segs = append(q.segs, last)
	}
	if _, err := q.w.Write(b.Bytes()); err != nil {
		// Keep the segment consistent with what was counted.
		q.w.Truncate(q.size[last])
		return err
	}
	q.size[last] += int64(b.Len())
	q.lines[last] += int64(len(recs))
	q.enforceLimit()
	select {
	case q.notify <- struct{}{}:
	default:
	}
	return nil
}

func (q *Queue) diskBytes() int64 {
	var n int64
	for _, s := range q.segs {
		n += q.size[s]
	}
	return n
}

// enforceLimit drops the oldest segments while the queue is too big, never
// the one being written.
func (q *Queue) enforceLimit() {
	if q.maxBytes <= 0 {
		return
	}
	for len(q.segs) > 1 && q.diskBytes() > q.maxBytes {
		s := q.segs[0]
		if s == q.cur.seg {
			q.drop += q.lines[s] - q.cur.line
			q.cur = pos{seg: q.segs[1]}
		} else {
			q.drop += q.lines[s]
		}
		q.removeSeg(s)
		q.saveCursor()
	}
}

// Peek returns up to maxRecs records (and about maxBytes) from the cursor
// without confirming them.
func (q *Queue) Peek(maxRecs, maxBytes int) ([]Item, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []Item
	total := 0
	p := q.cur
	for len(out) < maxRecs && total < maxBytes {
		if p.off >= q.size[p.seg] {
			i := slices.Index(q.segs, p.seg)
			if i < 0 || i == len(q.segs)-1 {
				break
			}
			p = pos{seg: q.segs[i+1]}
			continue
		}
		f, err := os.Open(filepath.Join(q.dir, segName(p.seg)))
		if err != nil {
			return out, err
		}
		if _, err := f.Seek(p.off, io.SeekStart); err != nil {
			f.Close()
			return out, err
		}
		r := bufio.NewReaderSize(io.LimitReader(f, q.size[p.seg]-p.off), 256<<10)
		for len(out) < maxRecs && total < maxBytes {
			line, err := r.ReadBytes('\n')
			if err != nil {
				break
			}
			p.off += int64(len(line))
			p.line++
			total += len(line)
			out = append(out, Item{Data: line[:len(line)-1], end: p})
		}
		f.Close()
	}
	return out, nil
}

// Ack confirms every record up to and including it.
func (q *Queue) Ack(it Item) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !slices.Contains(q.segs, it.end.seg) {
		return // dropped meanwhile because the queue was full
	}
	if it.end.seg < q.cur.seg || it.end.seg == q.cur.seg && it.end.off <= q.cur.off {
		return
	}
	q.cur = it.end
	for len(q.segs) > 1 && q.segs[0] < q.cur.seg {
		q.removeSeg(q.segs[0])
	}
	// A fully read segment that is no longer written to can go.
	if last := q.segs[len(q.segs)-1]; q.cur.seg != last && q.cur.off >= q.size[q.cur.seg] {
		next := q.segs[slices.Index(q.segs, q.cur.seg)+1]
		q.removeSeg(q.cur.seg)
		q.cur = pos{seg: next}
	}
	q.saveCursor()
}

func (q *Queue) saveCursor() {
	p := filepath.Join(q.dir, "cursor")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, fmt.Appendf(nil, "%d %d %d\n", q.cur.seg, q.cur.off, q.cur.line), 0o640); err == nil {
		os.Rename(tmp, p)
	}
}

// Pending returns the number and size of unconfirmed records.
func (q *Queue) Pending() (records, size int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, s := range q.segs {
		if s < q.cur.seg {
			continue
		}
		records += q.lines[s]
		size += q.size[s]
		if s == q.cur.seg {
			records -= q.cur.line
			size -= q.cur.off
		}
	}
	return records, size
}

// TakeDropped returns and resets the number of records dropped because the
// queue was full.
func (q *Queue) TakeDropped() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := q.drop
	q.drop = 0
	return n
}

// SetLimit changes the size limit.
func (q *Queue) SetLimit(maxBytes int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.maxBytes = maxBytes
	q.enforceLimit()
}

// Notify is signalled after records were appended.
func (q *Queue) Notify() <-chan struct{} { return q.notify }

func (q *Queue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.w.Close()
}
