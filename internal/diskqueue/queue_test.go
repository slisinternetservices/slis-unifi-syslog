package diskqueue

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func recs(from, n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = fmt.Appendf(nil, `{"n":%d}`, from+i)
	}
	return out
}

func TestQueueDeliveryAndRestart(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	q.segMax = 200 // force several segments
	for i := 0; i < 10; i++ {
		if err := q.Append(recs(i*5, 5)); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := q.Pending(); n != 50 {
		t.Fatalf("pending %d", n)
	}
	items, err := q.Peek(12, 1<<20)
	if err != nil || len(items) != 12 || string(items[11].Data) != `{"n":11}` {
		t.Fatalf("peek %d %v", len(items), err)
	}
	// Peeking again without an ack returns the same records.
	again, _ := q.Peek(1, 1<<20)
	if string(again[0].Data) != `{"n":0}` {
		t.Fatalf("re-peek %s", again[0].Data)
	}
	q.Ack(items[11])
	if n, _ := q.Pending(); n != 38 {
		t.Fatalf("pending after ack %d", n)
	}
	q.Close()

	// A crash in the middle of a write leaves half a line behind.
	segs, _ := filepath.Glob(filepath.Join(dir, "*.seg"))
	f, _ := os.OpenFile(segs[len(segs)-1], os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"n":9`)
	f.Close()

	q, err = Open(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := q.Pending(); n != 38 {
		t.Fatalf("pending after restart %d", n)
	}
	q.Append(recs(50, 1))
	var got []string
	for {
		items, _ := q.Peek(7, 1<<20)
		if len(items) == 0 {
			break
		}
		for _, it := range items {
			got = append(got, string(it.Data))
		}
		q.Ack(items[len(items)-1])
	}
	if len(got) != 39 || got[0] != `{"n":12}` || got[38] != `{"n":50}` {
		t.Fatalf("got %d records: first %s last %s", len(got), got[0], got[len(got)-1])
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.seg")); len(left) != 1 {
		t.Fatalf("confirmed segments not removed: %v", left)
	}
}

func TestQueueLimitDropsOldest(t *testing.T) {
	q, err := Open(t.TempDir(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	q.segMax = 250
	for i := 0; i < 40; i++ {
		q.Append(recs(i*3, 3))
	}
	n, size := q.Pending()
	if size > 1000 || n == 0 {
		t.Fatalf("pending %d records, %d bytes", n, size)
	}
	if d := q.TakeDropped(); d+n != 120 {
		t.Fatalf("dropped %d + pending %d != 120", d, n)
	}
	items, _ := q.Peek(1000, 1<<20)
	if string(items[len(items)-1].Data) != `{"n":119}` {
		t.Fatalf("newest record lost: %s", items[len(items)-1].Data)
	}
}
