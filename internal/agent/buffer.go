package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"pk95monitoring/internal/model"
)

// MaxBuffered is the number of undelivered reports kept (one day at the default interval).
const MaxBuffered = 1440

// Buffer keeps undelivered reports in memory and in a JSON-lines file next to the config, so
// they survive a restart. New reports are appended; the file is rewritten when reports are
// removed or it has grown well beyond the cap. When full, the oldest reports are dropped.
type Buffer struct {
	mu        sync.Mutex
	path      string
	max       int
	items     [][]byte // compact JSON, oldest first
	fileLines int      // lines currently in the file (may exceed len(items) until compaction)
}

// OpenBuffer loads the buffer file (missing file = empty buffer; corrupt lines are skipped).
func OpenBuffer(path string, max int) (*Buffer, error) {
	if max <= 0 {
		max = MaxBuffered
	}
	b := &Buffer{path: path, max: max}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		b.fileLines++
		if len(line) == 0 || !json.Valid(line) {
			continue
		}
		b.items = append(b.items, bytes.Clone(line))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(b.items) > max {
		b.items = b.items[len(b.items)-max:]
	}
	if b.fileLines != len(b.items) {
		if err := b.rewrite(); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// Len returns the number of buffered reports.
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.items)
}

// Add appends a report; beyond the cap the oldest one is dropped.
func (b *Buffer) Add(r model.AgentReport) error {
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items = append(b.items, line)
	if len(b.items) > b.max {
		b.items = slicesDropFront(b.items, len(b.items)-b.max)
	}
	// Compact once the file holds 10 % more lines than the cap; append otherwise.
	if b.fileLines+1 > b.max+b.max/10 {
		return b.rewrite()
	}
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(b.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	b.fileLines++
	return nil
}

// Flush hands the buffered reports to send, oldest first, with Buffered set. It stops at the
// first error that is worth retrying; reports the server rejects permanently are dropped.
// At most limit reports are handled per call (0 = all), so a long backlog does not delay the
// next live report. It returns the number of reports delivered. The lock is held while
// sending, so Add waits.
func (b *Buffer) Flush(limit int, send func(model.AgentReport) error) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	done, sent := 0, 0
	var ferr error
	for _, raw := range b.items {
		if limit > 0 && done >= limit {
			break
		}
		var r model.AgentReport
		if err := json.Unmarshal(raw, &r); err == nil {
			r.Buffered = true
			if err := send(r); err == nil {
				sent++
			} else if !IsPermanent(err) {
				ferr = err
				break
			}
		}
		done++
	}
	if done == 0 {
		return 0, ferr
	}
	b.items = slicesDropFront(b.items, done)
	if err := b.rewrite(); err != nil && ferr == nil {
		ferr = err
	}
	return sent, ferr
}

func slicesDropFront(s [][]byte, n int) [][]byte {
	out := make([][]byte, len(s)-n, max(len(s)-n, 16))
	copy(out, s[n:])
	return out
}

// rewrite replaces the file with the current items (caller holds the lock).
func (b *Buffer) rewrite() error {
	if len(b.items) == 0 {
		b.fileLines = 0
		if err := os.Remove(b.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, it := range b.items {
		buf.Write(it)
		buf.WriteByte('\n')
	}
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, b.path); err != nil {
		os.Remove(tmp)
		return err
	}
	b.fileLines = len(b.items)
	return nil
}
