//go:build linux || windows

package agent

import (
	"encoding/json"

	"pk95monitoring/internal/eventrules"
)

// setQueries stores the event log queries of the current rules; a change refreshes at once.
func (p *platformState) setQueries(q []eventrules.Query) {
	b, _ := json.Marshal(q)
	p.evMu.Lock()
	changed := string(b) != p.evKey
	if changed {
		p.evQueries, p.evKey = q, string(b)
	}
	p.evMu.Unlock()
	if changed {
		p.events.Invalidate()
	}
}
