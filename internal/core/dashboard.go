package core

import (
	"context"
	"slices"
	"strings"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

// Dashboard parts of the overview: totals, unreachable devices, checks with a problem and
// the events of the last hours.

// recentHours is how far back the dashboard lists events; recentMax bounds the list.
const (
	recentHours = 12
	recentMax   = 300
)

// Totals are the general numbers of the dashboard. Devices counts what the device list shows
// (attached agents belong to their device); checks count every device's checks except the
// reachability and the inventory, quiet ones included.
type Totals struct {
	Devices       int `json:"devices"`  // enabled devices
	Agents        int `json:"agents"`   // enabled agents (attached ones included)
	Disabled      int `json:"disabled"` // switched off
	Checks        int `json:"checks"`
	ChecksOK      int `json:"checks_ok"`
	ChecksWarn    int `json:"checks_warn"`
	ChecksCrit    int `json:"checks_crit"`
	ChecksUnknown int `json:"checks_unknown"`
	Offline       int `json:"offline"`     // unreachable, not in maintenance
	Maintenance   int `json:"maintenance"` // devices in maintenance
}

// DashCheck is a line of the dashboard: an unreachable device or a check with a problem.
type DashCheck struct {
	DeviceID   string `json:"device_id"`
	Device     string `json:"device"` // "<parent> / <agent>" for an attached agent
	DeviceType string `json:"device_type"`
	Folder     string `json:"folder,omitempty"`
	Key        string `json:"key"`
	Name       string `json:"name"`
	Group      string `json:"group,omitempty"`
	State      string `json:"state"`
	Message    string `json:"message"`
	Since      int64  `json:"since"`
	LastOK     int64  `json:"last_ok,omitempty"` // offline: last successful contact
	Acked      bool   `json:"acked,omitempty"`
}

// dashboard collects totals, offline devices and problem checks. Devices that are disabled
// or in maintenance are counted but not listed; quiet and new checks are never problems.
func (c *Core) dashboard(now int64) (Totals, []DashCheck, []DashCheck) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var t Totals
	offline, problems := []DashCheck{}, []DashCheck{}
	for _, d := range c.devices {
		top := !c.attached(d)
		if !d.Enabled {
			if top {
				t.Disabled++
			}
			continue
		}
		if top {
			t.Devices++
		}
		if d.Type == model.TypeAgent {
			t.Agents++
		}
		maint := d.InMaintenance(now)
		if maint && top {
			t.Maintenance++
		}
		rt := c.rt[d.ID]
		if rt == nil {
			continue
		}
		folder := d.Group
		if p, ok := c.parentOf(d); ok {
			folder = p.Group
		}
		line := func(cs model.CheckStatus) DashCheck {
			msg := cs.Reason
			if msg == "" {
				msg = cs.Message
			}
			return DashCheck{DeviceID: d.ID, Device: c.displayName(d), DeviceType: d.Type, Folder: folder, Key: cs.Key,
				Name: cs.Name, Group: cs.Group, State: cs.State, Message: msg, Since: cs.Since, Acked: cs.Acked}
		}
		bad := func(cs model.CheckStatus) bool {
			return !maint && cs.Mode == "" && (cs.State == model.StateWarn || cs.State == model.StateCrit)
		}
		for _, cs := range rt.st.Checks {
			switch cs.Key {
			case reachKey:
				if cs.State == model.StateCrit && !maint {
					t.Offline++
					l := line(cs)
					l.LastOK = rt.st.LastOK
					offline = append(offline, l)
				}
				continue
			case model.DiscoveryKey:
				if bad(cs) {
					problems = append(problems, line(cs))
				}
				continue
			}
			t.Checks++
			switch cs.State {
			case model.StateOK:
				t.ChecksOK++
			case model.StateWarn:
				t.ChecksWarn++
			case model.StateCrit:
				t.ChecksCrit++
			default:
				t.ChecksUnknown++
			}
			if bad(cs) {
				problems = append(problems, line(cs))
			}
		}
	}
	// the checks of an unreachable device are stale: its offline line says it all
	off := map[string]bool{}
	for _, o := range offline {
		off[o.DeviceID] = true
	}
	problems = slices.DeleteFunc(problems, func(p DashCheck) bool { return off[p.DeviceID] })
	slices.SortFunc(offline, func(a, b DashCheck) int { // longest outage first
		if a.Since != b.Since {
			return cmpInt(a.Since, b.Since)
		}
		return strings.Compare(strings.ToLower(a.Device), strings.ToLower(b.Device))
	})
	slices.SortFunc(problems, func(a, b DashCheck) int {
		// open before acknowledged, crit before warn, newest first
		if a.Acked != b.Acked {
			if a.Acked {
				return 1
			}
			return -1
		}
		if r := model.Rank(b.State) - model.Rank(a.State); r != 0 {
			return r
		}
		if a.Since != b.Since {
			return cmpInt(b.Since, a.Since)
		}
		return strings.Compare(strings.ToLower(a.Device+" "+a.Name), strings.ToLower(b.Device+" "+b.Name))
	})
	return t, offline, problems
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// recentEvents returns the events of the last recentHours hours (at most recentMax), newest first.
func (c *Core) recentEvents(ctx context.Context, now int64) ([]store.Event, error) {
	evs, err := c.St.Events(ctx, "", 0, recentMax)
	if err != nil {
		return nil, err
	}
	cut := now - recentHours*3600
	out := evs[:0]
	for _, e := range evs {
		if e.TS >= cut {
			out = append(out, e)
		}
	}
	return out, nil
}
