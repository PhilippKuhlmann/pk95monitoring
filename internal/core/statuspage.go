package core

import (
	"context"
	"crypto/subtle"
	"fmt"
	"slices"
	"strings"
	"time"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

// The status page is a public, read-only page for the customer behind a secret link
// (/status/<token>). It shows services the admin composes from devices and folders, with
// their current state, the history of the last statusDays days, the maintenance windows
// marked as public and an optional notice. It names no devices, hosts or checks: visitors
// only see what the admin wrote (service names, notes, window names and notes).

const keyStatusPage = "status_page" // settings key: StatusPage

const (
	statusDays          = 30                  // history shown per service
	statusAhead         = 14 * 24 * time.Hour // public maintenance announced this far ahead
	statusCacheTTL      = 30 * time.Second    // the public view is computed at most this often
	statusTokenLen      = 32
	maxStatusComponents = 30
	maxStatusTitle      = 80
	maxStatusText       = 500
	maxComponentName    = 80
	maxComponentNote    = 200
)

// Status page service modes: what makes a service look disturbed.
const (
	// StatusOutage: only outages count (a device unreachable or its agent silent; for a device
	// without a reachability check, e.g. network checks, a critical state); some of several
	// devices down is a partial outage.
	StatusOutage = "outage"
	// StatusAll: every problem counts: critical alerts are a disruption, warnings a limitation.
	StatusAll = "all"
)

// Public states of a service and of the whole page.
const (
	PubOK      = "ok"
	PubWarn    = "warn"  // limited or partial outage
	PubCrit    = "crit"  // disruption
	PubMaint   = "maint" // maintenance
	PubUnknown = "unknown"
	PubNone    = "none" // history: the service had no devices yet
)

// Notice levels.
var noticeLevels = []string{"info", "warn", "crit"}

// StatusPage is the configuration of the status page.
type StatusPage struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token"` // secret part of the link; set by the server
	Title   string `json:"title"` // "" = the instance name
	Intro   string `json:"intro"`
	// Notice is shown as a banner above the services (e.g. a provider outage), NoticeAt
	// is when it was last changed.
	Notice      string            `json:"notice"`
	NoticeLevel string            `json:"notice_level"` // info, warn, crit
	NoticeAt    int64             `json:"notice_at"`
	Components  []StatusComponent `json:"components"`
	Updated     int64             `json:"updated"`
}

// StatusComponent is one service of the status page.
type StatusComponent struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Note    string   `json:"note"`
	Mode    string   `json:"mode"` // StatusOutage, StatusAll
	Devices []string `json:"devices"`
	Folders []string `json:"folders"` // with subfolders; attached agents always count
}

func (p StatusPage) clone() StatusPage {
	p.Components = slices.Clone(p.Components)
	for i := range p.Components {
		p.Components[i].Devices = slices.Clone(p.Components[i].Devices)
		p.Components[i].Folders = slices.Clone(p.Components[i].Folders)
	}
	return p
}

// PublicStatus is the status page as its visitors get it.
type PublicStatus struct {
	Title       string            `json:"title"`
	Intro       string            `json:"intro,omitempty"`
	Notice      *StatusNotice     `json:"notice,omitempty"`
	State       string            `json:"state"` // worst service state
	Text        string            `json:"text"`
	Components  []PublicComponent `json:"components"`
	Maintenance []PublicMaint     `json:"maintenance"`
	Days        int               `json:"days"`
	Updated     int64             `json:"updated"`
}

// StatusNotice is the notice banner.
type StatusNotice struct {
	Level string `json:"level"`
	Text  string `json:"text"`
	At    int64  `json:"at"`
}

// PublicComponent is a service with its state and history.
type PublicComponent struct {
	Name  string `json:"name"`
	Note  string `json:"note,omitempty"`
	State string `json:"state"`
	Text  string `json:"text"`
	Since int64  `json:"since,omitempty"` // start of the problem
	Until int64  `json:"until,omitempty"` // end of the maintenance
	// Availability is the share of the covered time without a disruption (percent); nil
	// while the service has no devices.
	Availability *float64    `json:"availability"`
	History      []StatusDay `json:"history"` // oldest first, today last
	// Members is the number of devices behind the service (admin preview only).
	Members int `json:"members,omitempty"`
}

// StatusDay is one day of a service's history.
type StatusDay struct {
	Day     string `json:"day"`   // "2026-11-14", local time
	State   string `json:"state"` // ok, warn, crit, none
	Down    int64  `json:"down"`  // seconds with a disruption
	Limited int64  `json:"limited"`
}

// PublicMaint is a running or announced maintenance.
type PublicMaint struct {
	Name       string   `json:"name"`
	Note       string   `json:"note,omitempty"`
	Start      int64    `json:"start"`
	End        int64    `json:"end"`
	Running    bool     `json:"running"`
	Components []string `json:"components"` // names of the affected services
}

// StatusPage returns the configuration of the status page.
func (c *Core) StatusPage() StatusPage {
	c.spMu.Lock()
	defer c.spMu.Unlock()
	p := c.status.clone()
	if p.Components == nil {
		p.Components = []StatusComponent{}
	}
	return p
}

// newStatusToken returns a fresh secret for the link.
func newStatusToken() string {
	return randomString("abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789", statusTokenLen)
}

// SaveStatusPage validates and stores the configuration; the token is kept (created on the
// first save). Field errors are keyed like the JSON ("components.0.name").
func (c *Core) SaveStatusPage(p StatusPage) (StatusPage, map[string]string, error) {
	errs := map[string]string{}
	p.Title = strings.Join(strings.Fields(p.Title), " ")
	p.Intro = strings.TrimSpace(p.Intro)
	p.Notice = strings.TrimSpace(p.Notice)
	if len([]rune(p.Title)) > maxStatusTitle {
		errs["title"] = fmt.Sprintf("Höchstens %d Zeichen.", maxStatusTitle)
	}
	if len([]rune(p.Intro)) > maxStatusText {
		errs["intro"] = fmt.Sprintf("Höchstens %d Zeichen.", maxStatusText)
	}
	if len([]rune(p.Notice)) > maxStatusText {
		errs["notice"] = fmt.Sprintf("Höchstens %d Zeichen.", maxStatusText)
	}
	if p.NoticeLevel == "" {
		p.NoticeLevel = "info"
	}
	if !slices.Contains(noticeLevels, p.NoticeLevel) {
		errs["notice_level"] = "Ungültige Art."
	}
	if len(p.Components) > maxStatusComponents {
		errs["components"] = fmt.Sprintf("Höchstens %d Dienste.", maxStatusComponents)
	}
	if p.Components == nil {
		p.Components = []StatusComponent{}
	}
	if p.Enabled && len(p.Components) == 0 {
		errs["components"] = "Mindestens einen Dienst anlegen."
	}
	c.mu.Lock()
	ids := map[string]bool{}
	for i := range p.Components {
		cp := &p.Components[i]
		key := func(f string) string { return fmt.Sprintf("components.%d.%s", i, f) }
		cp.Name = strings.Join(strings.Fields(cp.Name), " ")
		cp.Note = strings.TrimSpace(cp.Note)
		switch {
		case cp.Name == "":
			errs[key("name")] = "Name fehlt."
		case len([]rune(cp.Name)) > maxComponentName:
			errs[key("name")] = fmt.Sprintf("Höchstens %d Zeichen.", maxComponentName)
		}
		if len([]rune(cp.Note)) > maxComponentNote {
			errs[key("note")] = fmt.Sprintf("Höchstens %d Zeichen.", maxComponentNote)
		}
		if cp.Mode == "" {
			cp.Mode = StatusOutage
		}
		if cp.Mode != StatusOutage && cp.Mode != StatusAll {
			errs[key("mode")] = "Ungültige Auswahl."
		}
		devs := []string{}
		for _, id := range cp.Devices {
			if _, ok := c.devices[id]; ok && !slices.Contains(devs, id) {
				devs = append(devs, id)
			}
		}
		cp.Devices = devs
		folders := []string{}
		for _, f := range cp.Folders {
			if f = NormalizeGroup(f); f != "" && !slices.Contains(folders, f) {
				folders = append(folders, f)
			}
		}
		slices.Sort(folders)
		cp.Folders = folders
		if len(cp.Devices)+len(cp.Folders) == 0 {
			errs[key("targets")] = "Mindestens ein Gerät oder einen Ordner wählen."
		}
		if cp.ID == "" || ids[cp.ID] || len(cp.ID) > 16 {
			cp.ID = newID(6)
		}
		ids[cp.ID] = true
	}
	c.mu.Unlock()
	if len(errs) > 0 {
		return StatusPage{}, errs, nil
	}

	c.spMu.Lock()
	now := c.Now().Unix()
	old := c.status
	p.Token = old.Token
	if p.Token == "" {
		p.Token = newStatusToken()
	}
	p.NoticeAt = old.NoticeAt
	if p.Notice != old.Notice || p.NoticeLevel != old.NoticeLevel {
		p.NoticeAt = now
	}
	if p.Notice == "" {
		p.NoticeAt = 0
	}
	p.Updated = now
	if err := c.St.SetSetting(keyStatusPage, p); err != nil {
		c.spMu.Unlock()
		return StatusPage{}, nil, err
	}
	c.status, c.spCache = p.clone(), nil
	c.spMu.Unlock()

	switch {
	case p.Enabled && !old.Enabled:
		c.Event("info", "Statusseite", "Statusseite eingeschaltet", "Die Statusseite ist über ihren Link erreichbar.", nil, false)
	case !p.Enabled && old.Enabled:
		c.Event("info", "Statusseite", "Statusseite ausgeschaltet", "Der Link der Statusseite zeigt nichts mehr an.", nil, false)
	}
	if p.Notice != old.Notice && p.Notice != "" && p.Enabled {
		c.Event("info", "Statusseite", "Hinweis auf der Statusseite", p.Notice, nil, false)
	}
	return p, nil, nil
}

// RotateStatusToken gives the status page a new link; the old one stops working.
func (c *Core) RotateStatusToken() (StatusPage, error) {
	c.spMu.Lock()
	p := c.status.clone()
	p.Token = newStatusToken()
	p.Updated = c.Now().Unix()
	if err := c.St.SetSetting(keyStatusPage, p); err != nil {
		c.spMu.Unlock()
		return StatusPage{}, err
	}
	c.status, c.spCache = p.clone(), nil
	c.spMu.Unlock()
	c.Event("info", "Statusseite", "Neuer Link für die Statusseite", "Der bisherige Link funktioniert nicht mehr.", nil, false)
	if p.Components == nil {
		p.Components = []StatusComponent{}
	}
	return p, nil
}

// StatusTokenValid reports whether token opens the status page (on and the right token).
func (c *Core) StatusTokenValid(token string) bool {
	c.spMu.Lock()
	defer c.spMu.Unlock()
	p := c.status
	return p.Enabled && p.Token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(p.Token)) == 1
}

// PublicStatus returns the status page for a visitor with token; store.ErrNotFound when the
// page is off or the token is wrong. The result is cached for statusCacheTTL.
func (c *Core) PublicStatus(ctx context.Context, token string) (PublicStatus, error) {
	if !c.StatusTokenValid(token) {
		return PublicStatus{}, store.ErrNotFound
	}
	c.spMu.Lock()
	p := c.status
	now := c.Now()
	if c.spCache != nil && now.Sub(c.spAt) < statusCacheTTL && now.Sub(c.spAt) >= 0 {
		out := *c.spCache
		c.spMu.Unlock()
		return out, nil
	}
	p = p.clone()
	c.spMu.Unlock()
	out, err := c.buildStatus(ctx, p, false)
	if err != nil {
		return PublicStatus{}, err
	}
	c.spMu.Lock()
	if c.status.Updated == p.Updated { // not changed meanwhile
		c.spCache, c.spAt = &out, now
	}
	c.spMu.Unlock()
	return out, nil
}

// StatusPreview computes the status page as configured (on or off) for the admin; the
// services also report how many devices they cover.
func (c *Core) StatusPreview(ctx context.Context) (PublicStatus, error) {
	return c.buildStatus(ctx, c.StatusPage(), true)
}

// member is a device behind a service with the alerts of the history period. reach: the
// device has a reachability check (net devices have none: their critical checks are the outage).
type member struct {
	d      model.Device
	alerts []model.Alert
	reach  bool
}

// buildStatus computes the public view of p.
func (c *Core) buildStatus(ctx context.Context, p StatusPage, preview bool) (PublicStatus, error) {
	now := c.Now().Local()
	nowU := now.Unix()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	first := today.AddDate(0, 0, -(statusDays - 1))
	alerts, err := c.St.AlertsBetween(ctx, first.Unix(), nowU+1)
	if err != nil {
		return PublicStatus{}, err
	}
	byDev := map[string][]model.Alert{}
	for _, a := range alerts {
		if !model.SeparateCheck(a.CheckKey) { // updates, event logs and backups have their own pages
			byDev[a.DeviceID] = append(byDev[a.DeviceID], a)
		}
	}
	out := PublicStatus{Title: p.Title, Intro: p.Intro, Days: statusDays, Updated: nowU,
		Components: []PublicComponent{}, Maintenance: []PublicMaint{}}
	if out.Title == "" {
		out.Title = c.InstanceName()
	}
	if p.Notice != "" {
		out.Notice = &StatusNotice{Level: p.NoticeLevel, Text: p.Notice, At: p.NoticeAt}
	}

	c.mu.Lock()
	members := make([][]member, len(p.Components))
	for i, cp := range p.Components {
		for _, d := range c.devices {
			if d.Enabled && c.targetMatches(cp.Devices, cp.Folders, d) {
				m := member{d: d, alerts: byDev[d.ID]}
				if rt := c.rt[d.ID]; rt != nil {
					m.reach = slices.ContainsFunc(rt.st.Checks, func(cs model.CheckStatus) bool { return cs.Key == reachKey })
				}
				members[i] = append(members[i], m)
			}
		}
		pc := c.componentNow(cp, members[i], nowU)
		pc.History, pc.Availability = componentHistory(cp, members[i], first, statusDays, nowU)
		if preview {
			pc.Members = len(members[i])
		}
		out.Components = append(out.Components, pc)
	}
	out.Maintenance = c.publicWindows(p, members, now)
	c.mu.Unlock()

	out.State, out.Text = overallState(out.Components)
	return out, nil
}

// targetMatches reports whether d is one of devs, in one of folders (or a subfolder) or
// attached to such a device. Caller holds c.mu.
func (c *Core) targetMatches(devs, folders []string, d model.Device) bool {
	match := func(x model.Device) bool {
		if slices.Contains(devs, x.ID) {
			return true
		}
		for _, f := range folders {
			if inGroup(x.Group, f) {
				return true
			}
		}
		return false
	}
	if match(d) {
		return true
	}
	if d.ParentID == "" {
		return false
	}
	p, ok := c.devices[d.ParentID]
	return ok && match(p)
}

// componentNow is the current state of a service. Caller holds c.mu.
func (c *Core) componentNow(cp StatusComponent, ms []member, now int64) PublicComponent {
	pc := PublicComponent{Name: cp.Name, Note: cp.Note}
	var down, limited, okN, maint, known int
	var since, until int64
	earliest := func(ts int64) {
		if ts > 0 && (since == 0 || ts < since) {
			since = ts
		}
	}
	for _, m := range ms {
		if m.d.InMaintenance(now) {
			maint++
			until = max(until, m.d.Maintenance.Until)
			continue
		}
		rt := c.rt[m.d.ID]
		if rt == nil {
			continue
		}
		if cp.Mode == StatusOutage {
			if i := slices.IndexFunc(rt.st.Checks, func(cs model.CheckStatus) bool { return cs.Key == reachKey }); i >= 0 {
				cs := rt.st.Checks[i]
				known++
				if cs.State == model.StateCrit {
					down++
					earliest(cs.Since)
				} else {
					okN++
				}
				continue
			}
		}
		switch rt.st.State {
		case model.StateCrit:
			known++
			if cp.Mode == StatusAll || !m.reach {
				down++
				earliest(rt.st.Since)
			} else {
				okN++ // a problem that is no outage
			}
		case model.StateWarn:
			known++
			if cp.Mode == StatusAll {
				limited++
				earliest(rt.st.Since)
			} else {
				okN++
			}
		case model.StateOK:
			known++
			okN++
		}
	}
	switch {
	case down > 0 && cp.Mode == StatusOutage && down < known:
		pc.State, pc.Text, pc.Since = PubWarn, "Teilweise gestört", since
	case down > 0:
		pc.State, pc.Text, pc.Since = PubCrit, "Störung", since
	case limited > 0:
		pc.State, pc.Text, pc.Since = PubWarn, "Eingeschränkt", since
	case maint > 0:
		pc.State, pc.Text, pc.Until = PubMaint, "Wartungsarbeiten", until
	case okN > 0:
		pc.State, pc.Text = PubOK, "Betriebsbereit"
	default:
		pc.State, pc.Text = PubUnknown, "Keine aktuellen Daten"
	}
	return pc
}

// componentHistory returns the daily history of a service over days days from first and its
// availability. Outages (reach alerts) are disruptions; with StatusAll critical alerts too,
// and warnings limitations. Several devices with a problem at once count once.
func componentHistory(cp StatusComponent, ms []member, first time.Time, days int, now int64) ([]StatusDay, *float64) {
	hist := make([]StatusDay, days)
	start := int64(0)
	for _, m := range ms {
		if start == 0 || m.d.Created < start {
			start = m.d.Created
		}
	}
	from := max(first.Unix(), start)
	var down, limited []span
	for _, m := range ms {
		for _, a := range m.alerts {
			end := a.Ended
			if end == 0 {
				end = now
			}
			s := span{max(a.Started, from), min(end, now)}
			if s.to <= s.from {
				continue
			}
			switch {
			case a.CheckKey == reachKey, (cp.Mode == StatusAll || !m.reach) && a.Severity == model.StateCrit:
				down = append(down, s)
			case cp.Mode == StatusAll:
				limited = append(limited, s)
			}
		}
	}
	for i := range hist {
		d0 := first.AddDate(0, 0, i)
		d1 := first.AddDate(0, 0, i+1)
		day := StatusDay{Day: d0.Format("2006-01-02"), State: PubNone}
		if len(ms) > 0 && d1.Unix() > start && d0.Unix() <= now {
			lo, hi := max(d0.Unix(), from), min(d1.Unix(), now)
			day.Down = overlap(down, lo, hi)
			day.Limited = overlap(limited, lo, hi)
			switch {
			case day.Down > 0:
				day.State = PubCrit
			case day.Limited > 0:
				day.State = PubWarn
			default:
				day.State = PubOK
			}
		}
		hist[i] = day
	}
	if len(ms) == 0 || now <= from {
		return hist, nil
	}
	av := 100 * float64(now-from-overlap(down, from, now)) / float64(now-from)
	return hist, &av
}

// overlap is the length of the union of spans within [lo, hi).
func overlap(list []span, lo, hi int64) int64 {
	if hi <= lo {
		return 0
	}
	cut := make([]span, 0, len(list))
	for _, s := range list {
		if s.from < hi && s.to > lo {
			cut = append(cut, span{max(s.from, lo), min(s.to, hi)})
		}
	}
	return unionLen(cut)
}

// publicWindows lists the public maintenance windows that run now or start within
// statusAhead, with the services they affect. Caller holds c.mu.
func (c *Core) publicWindows(p StatusPage, members [][]member, now time.Time) []PublicMaint {
	out := []PublicMaint{}
	for _, w := range c.windows {
		if !w.Enabled || !w.Public {
			continue
		}
		pm := PublicMaint{Name: w.Name, Note: w.Note, Components: []string{}}
		if s, e, ok := w.occurrence(now); ok {
			pm.Start, pm.End, pm.Running = s.Unix(), e.Unix(), true
		} else if s, ok := w.next(now); ok && s.Before(now.Add(statusAhead)) {
			pm.Start, pm.End = s.Unix(), s.Add(time.Duration(w.Minutes)*time.Minute).Unix()
		} else {
			continue
		}
		for i, cp := range p.Components {
			if slices.ContainsFunc(members[i], func(m member) bool { return c.windowMatches(w, m.d) }) {
				pm.Components = append(pm.Components, cp.Name)
			}
		}
		out = append(out, pm)
	}
	slices.SortFunc(out, func(a, b PublicMaint) int {
		if a.Start != b.Start {
			return int(a.Start - b.Start)
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// overallState is the state of the page: the worst service state (unknown services count
// only when nothing else is known).
func overallState(list []PublicComponent) (string, string) {
	if len(list) == 0 {
		return PubUnknown, "Noch keine Dienste eingerichtet"
	}
	var crit, warn, maint, ok int
	for _, pc := range list {
		switch pc.State {
		case PubCrit:
			crit++
		case PubWarn:
			warn++
		case PubMaint:
			maint++
		case PubOK:
			ok++
		}
	}
	switch {
	case crit > 0 && crit == len(list):
		return PubCrit, "Größere Störung"
	case crit > 0:
		return PubCrit, "Störung bei einzelnen Diensten"
	case warn > 0:
		return PubWarn, "Einzelne Dienste eingeschränkt"
	case maint > 0:
		return PubMaint, "Wartungsarbeiten laufen"
	case ok > 0:
		return PubOK, "Alle Dienste betriebsbereit"
	}
	return PubUnknown, "Keine aktuellen Daten"
}
