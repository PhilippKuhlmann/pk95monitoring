package core

import (
	"context"
	"log"
	"slices"
	"strings"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

// maxValueAge is how old a cpu/mem/disk value may be to be shown in lists (seconds).
const maxValueAge = 600

// Status returns the runtime status of a device (State "unknown" when never polled).
func (c *Core) Status(id string) model.DeviceStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rt := c.rt[id]; rt != nil {
		st := cloneStatus(rt.st)
		c.agentFacts(c.devices[id], &st)
		return st
	}
	return model.DeviceStatus{DeviceID: id, State: model.StateUnknown, Checks: []model.CheckStatus{}}
}

func countChecks(checks []model.CheckStatus) CheckCounts {
	var n CheckCounts
	for _, cs := range checks {
		switch cs.State {
		case model.StateOK:
			n.OK++
		case model.StateWarn:
			n.Warn++
		case model.StateCrit:
			n.Crit++
		default:
			n.Unknown++
		}
	}
	return n
}

// latestValues returns the newest value per device of a metric (max over instances),
// ignoring values older than maxValueAge.
func (c *Core) latestValues() map[string]map[string]float64 {
	out := map[string]map[string]float64{}
	list, err := c.St.LatestSeries("cpu.usage", "mem.usage", "disk.usage")
	if err != nil {
		log.Printf("latest series: %v", err)
		return out
	}
	limit := c.Now().Unix() - maxValueAge
	for _, s := range list {
		if s.LastTS < limit {
			continue
		}
		m := out[s.DeviceID]
		if m == nil {
			m = map[string]float64{}
			out[s.DeviceID] = m
		}
		if v, ok := m[s.Name]; !ok || s.LastValue > v {
			m[s.Name] = s.LastValue
		}
	}
	return out
}

func valuePtr(m map[string]float64, name string) *float64 {
	if v, ok := m[name]; ok {
		return &v
	}
	return nil
}

// Rows returns the list view of the top-level devices: agents attached to another device
// are left out, their parent's state, message and check counts include them.
func (c *Core) Rows() []DeviceRow { return c.rows(false) }

// AllRows returns the list view of every device, attached agents included (with ParentID and
// ParentName and their own state).
func (c *Core) AllRows() []DeviceRow { return c.rows(true) }

func (c *Core) rows(all bool) []DeviceRow {
	vals := c.latestValues()
	devs := c.Devices()
	now := c.Now().Unix()
	rows := make([]DeviceRow, 0, len(devs))
	c.mu.Lock()
	defer c.mu.Unlock()
	kids := c.childMap()
	for _, d := range devs {
		if !all && c.attached(d) {
			continue
		}
		rows = append(rows, c.row(d, vals, kids[d.ID], now))
	}
	return rows
}

// row builds the list entry of a device (with its attached agents kids). vals are the latest
// cpu/mem/disk values per device. Caller holds c.mu.
func (c *Core) row(d model.Device, vals map[string]map[string]float64, kids []model.Device, now int64) DeviceRow {
	own := vals[d.ID]
	r := DeviceRow{ID: d.ID, Name: d.Name, Type: d.Type, Host: d.Host, Group: d.Group, Enabled: d.Enabled,
		NoAlerts: d.NoAlerts, State: model.StateUnknown, Attached: len(kids),
		CPU: valuePtr(own, "cpu.usage"), Mem: valuePtr(own, "mem.usage"), Disk: valuePtr(own, "disk.usage")}
	if d.Maintenance != nil {
		r.MaintenanceUntil = d.Maintenance.Until
	}
	if p, ok := c.parentOf(d); ok {
		r.ParentID, r.ParentName = p.ID, p.Name
	}
	cb := c.combine(d, kids, now)
	r.State, r.Message, r.Since, r.LastPoll, r.Checks = cb.state, cb.message, cb.since, cb.lastPoll, cb.checks
	// A parent without values of its own (SNMP, net) shows the highest value of its agents.
	if own == nil {
		for _, k := range kids {
			r.CPU = maxPtr(r.CPU, vals[k.ID], "cpu.usage")
			r.Mem = maxPtr(r.Mem, vals[k.ID], "mem.usage")
			r.Disk = maxPtr(r.Disk, vals[k.ID], "disk.usage")
		}
	}
	if d.Agent != nil {
		r.OS, r.Platform, r.AgentVersion = d.Agent.OS, platformText(*d.Agent), d.Agent.Version
	}
	return r
}

// maxPtr returns the larger of cur and m[name].
func maxPtr(cur *float64, m map[string]float64, name string) *float64 {
	v := valuePtr(m, name)
	if v == nil || (cur != nil && *cur >= *v) {
		return cur
	}
	return v
}

// Groups returns the distinct, non-empty groups of the top-level devices, sorted.
func (c *Core) Groups() []string {
	c.mu.Lock()
	seen := map[string]bool{}
	out := []string{}
	for _, d := range c.devices {
		if d.Group != "" && !seen[d.Group] && !c.attached(d) {
			seen[d.Group] = true
			out = append(out, d.Group)
		}
	}
	c.mu.Unlock()
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out
}

// countRow adds a device to the counts: disabled > maintenance > state.
func (n *Counts) add(r DeviceRow, now int64) {
	switch {
	case !r.Enabled:
		n.Disabled++
	case r.MaintenanceUntil > now:
		n.Maintenance++
	default:
		switch r.State {
		case model.StateOK:
			n.OK++
		case model.StateWarn:
			n.Warn++
		case model.StateCrit:
			n.Crit++
		default:
			n.Unknown++
		}
	}
}

// Overview returns the dashboard data.
func (c *Core) Overview(ctx context.Context) (Overview, error) {
	rows := c.Rows()
	now := c.Now().Unix()
	var n Counts
	for _, r := range rows {
		n.add(r, now)
	}
	alerts, err := c.St.ActiveAlerts()
	if err != nil {
		return Overview{}, err
	}
	events, err := c.St.Events(ctx, "", 0, 10)
	if err != nil {
		return Overview{}, err
	}
	if alerts == nil {
		alerts = []model.Alert{}
	}
	if events == nil {
		events = []store.Event{}
	}
	return Overview{Counts: n, Devices: rows, Alerts: alerts, Events: events, Groups: c.Groups()}, nil
}

// deviceSummary is a device of the external API; a parent includes its attached agents
// (kids) like in the lists. Caller holds c.mu.
func (c *Core) deviceSummary(d model.Device, kids []model.Device, now int64) model.DeviceSummary {
	s := model.DeviceSummary{ID: d.ID, Name: d.Name, Type: d.Type, Group: d.Group, Host: d.Host, Enabled: d.Enabled,
		Maintenance: d.InMaintenance(now), State: model.StateUnknown, Checks: map[string]int{}}
	if rt := c.rt[d.ID]; rt != nil {
		cb := c.combine(d, kids, now)
		s.State, s.Message, s.LastSeen = cb.state, cb.message, rt.st.LastOK
		n := cb.checks
		s.Checks = map[string]int{"ok": n.OK, "warn": n.Warn, "crit": n.Crit, "unknown": n.Unknown}
	}
	if d.Agent != nil && d.Agent.LastSeen > 0 {
		s.LastSeen = d.Agent.LastSeen
	}
	return s
}

// Summary returns the summary for the external API and the central push. It lists the
// top-level devices (attached agents are part of their parent), each counted once.
func (c *Core) Summary() model.Summary {
	now := c.Now().Unix()
	devs := c.Devices()
	var n Counts
	sum := model.Summary{Instance: c.InstanceName(), Version: c.Opt.Version, URL: c.PublicURL(), Time: now,
		Devices: make([]model.DeviceSummary, 0, len(devs))}
	worst := ""
	c.mu.Lock()
	kids := c.childMap()
	for _, d := range devs {
		if c.attached(d) {
			continue
		}
		ds := c.deviceSummary(d, kids[d.ID], now)
		sum.Devices = append(sum.Devices, ds)
		n.add(c.row(d, nil, kids[d.ID], now), now)
		if d.Enabled && !ds.Maintenance && ds.State != model.StateUnknown {
			worst = model.Worst(worst, ds.State)
		}
	}
	c.mu.Unlock()
	switch {
	case worst != "":
		sum.State = worst
	case n.Unknown > 0:
		sum.State = model.StateUnknown
	default:
		sum.State = model.StateOK
	}
	sum.Counts = map[string]int{"ok": n.OK, "warn": n.Warn, "crit": n.Crit, "unknown": n.Unknown,
		"maintenance": n.Maintenance, "disabled": n.Disabled}
	alerts, err := c.St.ActiveAlerts()
	if err != nil {
		log.Printf("summary alerts: %v", err)
	}
	if alerts == nil {
		alerts = []model.Alert{}
	}
	sum.Alerts = alerts
	return sum
}

// DeviceSummary returns one device of the external API, or store.ErrNotFound.
func (c *Core) DeviceSummary(id string) (model.DeviceSummary, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.devices[id]
	if !ok {
		return model.DeviceSummary{}, store.ErrNotFound
	}
	return c.deviceSummary(d, c.childrenOf(d.ID), c.Now().Unix()), nil
}

// AckAlert acknowledges an alert.
func (c *Core) AckAlert(id int64, user string) error {
	now := c.Now().Unix()
	if err := c.St.AckAlert(id, user, now); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, oa := range c.open {
		if oa.a.ID == id {
			oa.a.AckedBy, oa.a.AckedAt = user, now
		}
	}
	return nil
}
