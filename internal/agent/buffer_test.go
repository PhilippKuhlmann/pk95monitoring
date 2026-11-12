package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pk95monitoring/internal/model"
)

func rep(t int64) model.AgentReport {
	return model.AgentReport{AgentID: "a", Time: t, Checks: []model.Check{{Key: "cpu", State: model.StateOK}}}
}

func times(t *testing.T, b *Buffer) []int64 {
	t.Helper()
	var out []int64
	_, err := b.Flush(0, func(r model.AgentReport) error {
		if !r.Buffered {
			t.Error("flushed report without buffered flag")
		}
		out = append(out, r.Time)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBufferPersistsInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buffer.jsonl")
	b, err := OpenBuffer(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 3; i++ {
		if err := b.Add(rep(i)); err != nil {
			t.Fatal(err)
		}
	}
	b2, err := OpenBuffer(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if b2.Len() != 3 {
		t.Fatalf("reloaded %d reports, want 3", b2.Len())
	}
	got := times(t, b2)
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("order %v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("empty buffer should remove its file")
	}
}

func TestBufferCapDropsOldest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buffer.jsonl")
	b, err := OpenBuffer(path, 5)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 12; i++ {
		if err := b.Add(rep(i)); err != nil {
			t.Fatal(err)
		}
	}
	if b.Len() != 5 {
		t.Fatalf("len %d, want 5", b.Len())
	}
	// The file may hold a few more lines until compaction; reloading must still give the newest 5.
	b2, err := OpenBuffer(path, 5)
	if err != nil {
		t.Fatal(err)
	}
	got := times(t, b2)
	want := []int64{8, 9, 10, 11, 12}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestBufferSkipsCorruptLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buffer.jsonl")
	data := `{"agent_id":"a","time":1,"checks":[]}` + "\n" + `{broken` + "\n\n" + `{"agent_id":"a","time":2,"checks":[]}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := OpenBuffer(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if b.Len() != 2 {
		t.Fatalf("len %d, want 2", b.Len())
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "broken") {
		t.Error("corrupt line not compacted away")
	}
}

func TestBufferFlushStopsOnRetryableError(t *testing.T) {
	b, err := OpenBuffer(filepath.Join(t.TempDir(), "buffer.jsonl"), 10)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 4; i++ {
		_ = b.Add(rep(i))
	}
	calls := 0
	n, err := b.Flush(0, func(r model.AgentReport) error {
		calls++
		switch r.Time {
		case 2:
			return &StatusError{Code: 400} // permanent: dropped
		case 3:
			return errors.New("connection refused") // retry later
		}
		return nil
	})
	if err == nil || n != 1 || calls != 3 {
		t.Fatalf("n=%d calls=%d err=%v", n, calls, err)
	}
	got := times(t, b)
	if len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("remaining %v, want [3 4]", got)
	}
}

func TestBufferFlushLimit(t *testing.T) {
	b, _ := OpenBuffer(filepath.Join(t.TempDir(), "buffer.jsonl"), 10)
	for i := int64(1); i <= 5; i++ {
		_ = b.Add(rep(i))
	}
	n, err := b.Flush(2, func(model.AgentReport) error { return nil })
	if err != nil || n != 2 || b.Len() != 3 {
		t.Fatalf("n=%d len=%d err=%v", n, b.Len(), err)
	}
	if got := times(t, b); got[0] != 3 {
		t.Errorf("remaining start at %d, want 3", got[0])
	}
}

func TestBufferUnauthorizedIsRetryable(t *testing.T) {
	b, _ := OpenBuffer(filepath.Join(t.TempDir(), "buffer.jsonl"), 10)
	_ = b.Add(rep(1))
	_, err := b.Flush(0, func(model.AgentReport) error { return ErrUnauthorized })
	if err == nil || b.Len() != 1 {
		t.Fatalf("401 must keep the report: err=%v len=%d", err, b.Len())
	}
}
