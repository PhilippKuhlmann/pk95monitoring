package fakepve

import (
	"testing"
	"time"
)

// TestNightlyJobsInWindow checks that every node's nightly backup job has finished and
// lies inside the last 24 h at every time of day (it used to vanish shortly after 01:00).
func TestNightlyJobsInWindow(t *testing.T) {
	s := New()
	defer s.Close()
	day := time.Date(2026, 3, 10, 0, 0, 0, 0, time.Local)
	for m := 0; m < 24*60; m += 7 {
		now := day.Add(time.Duration(m) * time.Minute)
		s.mu.Lock()
		list := s.tasks(now)
		s.mu.Unlock()
		nightly := map[string]bool{}
		for _, tk := range list {
			if tk.typ != "vzdump" || tk.id != "" {
				continue
			}
			if tk.start.Before(now.Add(-24*time.Hour)) || tk.end.After(now) {
				t.Errorf("%s: job of %s runs %s–%s, outside the last 24 h", now.Format("15:04"), tk.node,
					tk.start.Format("02. 15:04"), tk.end.Format("02. 15:04"))
			}
			nightly[tk.node] = true
		}
		if len(nightly) != len(s.nodes) {
			t.Errorf("%s: nightly jobs of %d nodes, want %d", now.Format("15:04"), len(nightly), len(s.nodes))
		}
	}
}
