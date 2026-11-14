package core

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

// Maintenance windows put devices into maintenance on a schedule (once, daily, weekly or
// monthly, in local time). While an occurrence runs, every device it applies to – listed
// devices, devices in the listed folders (and subfolders) and the agents attached to them –
// gets a maintenance until the end of the occurrence (model.Maintenance with Window and
// Start), so lists, alerts and reports treat it like a maintenance set by hand. A longer
// maintenance that is already running is kept; ending the maintenance of a device by hand
// holds for the rest of the occurrence.

// KindWindow is the object kind of MaintWindow.
const KindWindow = "window"

// Repeat values of a maintenance window.
const (
	RepeatOnce    = "once"
	RepeatDaily   = "daily"
	RepeatWeekly  = "weekly"
	RepeatMonthly = "monthly"
)

const (
	maxWindowMinutes = 7 * 24 * 60
	maxWindowName    = 80
	maxWindowNote    = 200
	weekLast         = 5 // MaintWindow.Week: the last weekday of the month
)

// MaintWindow is a recurring (or one-time) maintenance window.
type MaintWindow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Repeat  string `json:"repeat"`         // once, daily, weekly, monthly
	Date    string `json:"date,omitempty"` // once: "2026-11-20"
	// Weekdays: weekly – the days (0 = Sunday … 6 = Saturday); monthly with Week – one day.
	Weekdays []int `json:"weekdays"`
	// Week (monthly): 1–4 = first … fourth, 5 = last Weekdays[0] of the month; 0 = on Day.
	Week int `json:"week,omitempty"`
	// Day (monthly without Week): day of the month 1–31 (the last day in shorter months).
	Day int `json:"day,omitempty"`
	// Offset (monthly with Week) shifts the date by days, e.g. 2 = Thursday after the
	// second Tuesday (Microsoft's patch day).
	Offset  int      `json:"offset,omitempty"`
	Start   string   `json:"start"`   // "22:00", local time
	Minutes int      `json:"minutes"` // duration
	Devices []string `json:"devices"`
	Folders []string `json:"folders"`
	Note    string   `json:"note"`
	Created int64    `json:"created"`
	Updated int64    `json:"updated"`
}

// WindowView is a maintenance window as the API lists it.
type WindowView struct {
	MaintWindow
	Schedule    string `json:"schedule"`     // German description of the schedule
	ActiveUntil int64  `json:"active_until"` // end of the running occurrence; 0 = not running
	Next        int64  `json:"next"`         // start of the next occurrence; 0 = none
	Matches     int    `json:"matches"`      // devices it applies to (attached agents included)
}

var (
	dayNames    = [...]string{"Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"}
	dayShort    = [...]string{"So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"}
	weekOrdinal = [...]string{"", "1.", "2.", "3.", "4.", "letzten"}
)

// parseClock parses "HH:MM".
func parseClock(s string) (h, m int, ok bool) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, false
	}
	return t.Hour(), t.Minute(), true
}

// occursOn reports whether an occurrence starts on the day of t.
func (w MaintWindow) occursOn(t time.Time) bool {
	switch w.Repeat {
	case RepeatOnce:
		return t.Format("2006-01-02") == w.Date
	case RepeatDaily:
		return true
	case RepeatWeekly:
		return slices.Contains(w.Weekdays, int(t.Weekday()))
	case RepeatMonthly:
		if w.Week == 0 {
			last := time.Date(t.Year(), t.Month()+1, 0, 12, 0, 0, 0, t.Location()).Day()
			return t.Day() == min(w.Day, last)
		}
		if len(w.Weekdays) != 1 {
			return false
		}
		b := t.AddDate(0, 0, -w.Offset)
		if int(b.Weekday()) != w.Weekdays[0] {
			return false
		}
		if w.Week == weekLast {
			return b.AddDate(0, 0, 7).Month() != b.Month()
		}
		return (b.Day()-1)/7+1 == w.Week
	}
	return false
}

// startOn is the start of the occurrence on the day of t.
func (w MaintWindow) startOn(t time.Time) time.Time {
	h, m, _ := parseClock(w.Start)
	return time.Date(t.Year(), t.Month(), t.Day(), h, m, 0, 0, t.Location())
}

// occurrence returns the occurrence running at now (the latest one if several overlap).
func (w MaintWindow) occurrence(now time.Time) (start, end time.Time, ok bool) {
	dur := time.Duration(w.Minutes) * time.Minute
	day := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	for k := 0; k <= w.Minutes/1440+1; k++ {
		d := day.AddDate(0, 0, -k)
		if !w.occursOn(d) {
			continue
		}
		s := w.startOn(d)
		if !now.Before(s) && now.Before(s.Add(dur)) {
			return s, s.Add(dur), true
		}
	}
	return time.Time{}, time.Time{}, false
}

// next returns the start of the next occurrence after now (looking about two years ahead).
func (w MaintWindow) next(now time.Time) (time.Time, bool) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	for k := 0; k <= 800; k++ {
		d := day.AddDate(0, 0, k)
		if w.occursOn(d) {
			if s := w.startOn(d); s.After(now) {
				return s, true
			}
		}
	}
	return time.Time{}, false
}

// Describe is the schedule in German, e.g. "2 Tage nach dem 2. Dienstag im Monat, 22:00 Uhr,
// 4 Std.".
func (w MaintWindow) Describe() string {
	var when string
	switch w.Repeat {
	case RepeatOnce:
		if t, err := time.Parse("2006-01-02", w.Date); err == nil {
			when = "Einmalig am " + t.Format("02.01.2006")
		}
	case RepeatDaily:
		when = "Täglich"
	case RepeatWeekly:
		if len(w.Weekdays) == 7 {
			when = "Täglich"
			break
		}
		days := make([]string, 0, len(w.Weekdays))
		for _, d := range weekOrder(w.Weekdays) {
			days = append(days, dayShort[d])
		}
		when = "Jeden " + strings.Join(days, ", ")
	case RepeatMonthly:
		switch {
		case w.Week == 0:
			when = fmt.Sprintf("Monatlich am %d.", w.Day)
		case len(w.Weekdays) == 1:
			nth := fmt.Sprintf("%s %s im Monat", weekOrdinal[w.Week], dayNames[w.Weekdays[0]])
			switch {
			case w.Offset > 0:
				when = fmt.Sprintf("%d %s nach dem %s", w.Offset, plural1(w.Offset, "Tag", "Tage"), nth)
			case w.Offset < 0:
				when = fmt.Sprintf("%d %s vor dem %s", -w.Offset, plural1(-w.Offset, "Tag", "Tage"), nth)
			case w.Week == weekLast:
				when = "Am " + nth
			default:
				when = "Jeden " + nth
			}
		}
	}
	return fmt.Sprintf("%s, %s Uhr, %s", when, w.Start, durText(int64(w.Minutes)*60))
}

// weekOrder sorts weekdays Monday first.
func weekOrder(days []int) []int {
	out := slices.Clone(days)
	slices.SortFunc(out, func(a, b int) int { return (a+6)%7 - (b+6)%7 })
	return out
}

// validateWindow normalizes a window and returns German field errors. Caller holds c.mu.
func (c *Core) validateWindow(w *MaintWindow) map[string]string {
	errs := map[string]string{}
	w.Name = strings.Join(strings.Fields(w.Name), " ")
	w.Note = strings.TrimSpace(w.Note)
	switch {
	case w.Name == "":
		errs["name"] = "Name fehlt."
	case len([]rune(w.Name)) > maxWindowName:
		errs["name"] = fmt.Sprintf("Höchstens %d Zeichen.", maxWindowName)
	}
	if len([]rune(w.Note)) > maxWindowNote {
		errs["note"] = fmt.Sprintf("Höchstens %d Zeichen.", maxWindowNote)
	}
	w.Start = strings.TrimSpace(w.Start)
	if h, m, ok := parseClock(w.Start); ok {
		w.Start = fmt.Sprintf("%02d:%02d", h, m)
	} else {
		errs["start"] = "Uhrzeit im Format HH:MM angeben."
	}
	if w.Minutes < 5 || w.Minutes > maxWindowMinutes {
		errs["minutes"] = "Dauer zwischen 5 Minuten und 7 Tagen angeben."
	}
	days := []int{}
	for _, d := range w.Weekdays {
		if d < 0 || d > 6 {
			errs["weekdays"] = "Ungültiger Wochentag."
		} else if !slices.Contains(days, d) {
			days = append(days, d)
		}
	}
	w.Weekdays = weekOrder(days)
	switch w.Repeat {
	case RepeatOnce:
		w.Weekdays, w.Week, w.Day, w.Offset = []int{}, 0, 0, 0
		if _, err := time.Parse("2006-01-02", w.Date); err != nil {
			errs["date"] = "Datum fehlt."
		}
	case RepeatDaily:
		w.Date, w.Weekdays, w.Week, w.Day, w.Offset = "", []int{}, 0, 0, 0
	case RepeatWeekly:
		w.Date, w.Week, w.Day, w.Offset = "", 0, 0, 0
		if len(w.Weekdays) == 0 && errs["weekdays"] == "" {
			errs["weekdays"] = "Mindestens einen Wochentag wählen."
		}
	case RepeatMonthly:
		w.Date = ""
		switch {
		case w.Week < 0 || w.Week > weekLast:
			errs["week"] = "Ungültige Woche."
		case w.Week == 0:
			w.Weekdays, w.Offset = []int{}, 0
			if w.Day < 1 || w.Day > 31 {
				errs["day"] = "Tag zwischen 1 und 31 angeben."
			}
		default:
			w.Day = 0
			if len(w.Weekdays) != 1 && errs["weekdays"] == "" {
				errs["weekdays"] = "Genau einen Wochentag wählen."
			}
			if w.Offset < -7 || w.Offset > 14 {
				errs["offset"] = "Versatz zwischen -7 und 14 Tagen angeben."
			}
		}
	default:
		errs["repeat"] = "Wiederholung wählen."
	}
	devs := []string{}
	for _, id := range w.Devices {
		if _, ok := c.devices[id]; ok && !slices.Contains(devs, id) {
			devs = append(devs, id)
		}
	}
	w.Devices = devs
	folders := []string{}
	for _, f := range w.Folders {
		if f = NormalizeGroup(f); f != "" && !slices.Contains(folders, f) {
			folders = append(folders, f)
		}
	}
	slices.Sort(folders)
	w.Folders = folders
	if len(w.Devices)+len(w.Folders) == 0 {
		errs["targets"] = "Mindestens ein Gerät oder einen Ordner wählen."
	}
	return errs
}

// inGroup reports whether group g is folder f or one of its subfolders.
func inGroup(g, f string) bool { return g == f || strings.HasPrefix(g, f+"/") }

// windowMatches reports whether a window applies to device d (directly, by folder or through
// its parent). Caller holds c.mu.
func (c *Core) windowMatches(w MaintWindow, d model.Device) bool {
	match := func(x model.Device) bool {
		if slices.Contains(w.Devices, x.ID) {
			return true
		}
		for _, f := range w.Folders {
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

// windowView builds the API view of a window. Caller holds c.mu.
func (c *Core) windowView(w MaintWindow, now time.Time) WindowView {
	v := WindowView{MaintWindow: w, Schedule: w.Describe()}
	if w.Enabled {
		if _, e, ok := w.occurrence(now); ok {
			v.ActiveUntil = e.Unix()
		}
		if s, ok := w.next(now); ok {
			v.Next = s.Unix()
		}
	}
	for _, d := range c.devices {
		if c.windowMatches(w, d) {
			v.Matches++
		}
	}
	return v
}

// Windows lists the maintenance windows (by name).
func (c *Core) Windows() []WindowView {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now().Local()
	out := make([]WindowView, 0, len(c.windows))
	for _, w := range c.windows {
		out = append(out, c.windowView(w, now))
	}
	slices.SortFunc(out, func(a, b WindowView) int {
		if r := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); r != 0 {
			return r
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// DeviceWindows lists the enabled windows that apply to a device (by next start).
func (c *Core) DeviceWindows(id string) []WindowView {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []WindowView{}
	d, ok := c.devices[id]
	if !ok {
		return out
	}
	now := c.Now().Local()
	for _, w := range c.windows {
		if w.Enabled && c.windowMatches(w, d) {
			out = append(out, c.windowView(w, now))
		}
	}
	slices.SortFunc(out, func(a, b WindowView) int { return int(a.Next - b.Next) })
	return out
}

// SaveWindow creates (empty ID) or updates a maintenance window. Changing a window ends the
// maintenance it set; the new schedule is applied at once.
func (c *Core) SaveWindow(w MaintWindow) (WindowView, map[string]string, error) {
	c.mu.Lock()
	now := c.Now()
	if w.ID != "" {
		old, ok := c.windows[w.ID]
		if !ok {
			c.mu.Unlock()
			return WindowView{}, nil, store.ErrNotFound
		}
		w.Created = old.Created
	}
	if errs := c.validateWindow(&w); len(errs) > 0 {
		c.mu.Unlock()
		return WindowView{}, errs, nil
	}
	created := w.ID == ""
	if created {
		w.ID, w.Created = newID(8), now.Unix()
	}
	w.Updated = now.Unix()
	if err := c.St.Put(KindWindow, w.ID, w); err != nil {
		c.mu.Unlock()
		return WindowView{}, nil, err
	}
	c.windows[w.ID] = w
	c.releaseWindow(w.ID, now.Unix())
	delete(c.winActive, w.ID)
	c.mu.Unlock()
	title := "Wartungsfenster geändert"
	if created {
		title = "Wartungsfenster angelegt"
	}
	c.Event("info", "Wartungsfenster", title, w.Name+": "+w.Describe()+".", nil, false)
	c.applyWindows()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.windowView(c.windows[w.ID], c.Now().Local()), nil, nil
}

// DeleteWindow removes a maintenance window and ends the maintenance it set.
func (c *Core) DeleteWindow(id string) error {
	c.mu.Lock()
	w, ok := c.windows[id]
	if !ok {
		c.mu.Unlock()
		return store.ErrNotFound
	}
	if err := c.St.Delete(KindWindow, id); err != nil {
		c.mu.Unlock()
		return err
	}
	delete(c.windows, id)
	delete(c.winActive, id)
	ended := c.releaseWindow(id, c.Now().Unix())
	c.mu.Unlock()
	body := w.Name + "."
	if ended > 0 {
		body += fmt.Sprintf(" Die Wartung von %d %s ist beendet.", ended, plural1(ended, "Gerät", "Geräten"))
	}
	c.Event("info", "Wartungsfenster", "Wartungsfenster gelöscht", body, nil, false)
	return nil
}

// releaseWindow removes the maintenance window id set on devices and returns how many were
// still in it. Caller holds c.mu.
func (c *Core) releaseWindow(id string, now int64) int {
	n := 0
	for did, d := range c.devices {
		if d.Maintenance == nil || d.Maintenance.Window != id {
			continue
		}
		if d.Maintenance.Until > now {
			n++
		}
		d.Maintenance, d.Updated = nil, now
		if err := c.St.Put(KindDevice, did, d); err != nil {
			continue
		}
		c.devices[did] = d.Clone()
		if rt := c.rt[did]; rt != nil {
			c.syncAlerts(d, rt, now)
		}
	}
	return n
}

// applyWindows starts the maintenance of the running window occurrences (called every
// silence period and after a window changed) and logs when an occurrence begins or ends.
func (c *Core) applyWindows() {
	type ev struct{ title, body string }
	var evs []ev
	c.mu.Lock()
	now := c.Now().Local()
	ids := make([]string, 0, len(c.windows))
	for id := range c.windows {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		w := c.windows[id]
		start, end, ok := w.occurrence(now)
		if !w.Enabled || !ok {
			if c.winActive[id] != 0 {
				delete(c.winActive, id)
				evs = append(evs, ev{"Wartungsfenster beendet", w.Name + ": Alarme sind wieder aktiv."})
			}
			continue
		}
		note := "Wartungsfenster „" + w.Name + "“"
		if w.Note != "" {
			note += " – " + w.Note
		}
		n := 0
		for did, d := range c.devices {
			if !c.windowMatches(w, d) {
				continue
			}
			m := d.Maintenance
			if m != nil && m.Window == id && m.Start == start.Unix() {
				continue // set already, or ended by hand for this occurrence
			}
			if m != nil && m.Until >= end.Unix() {
				continue // a longer maintenance is running
			}
			d.Maintenance = &model.Maintenance{Until: end.Unix(), Note: note, Window: id, Start: start.Unix()}
			d.Updated = now.Unix()
			if err := c.St.Put(KindDevice, did, d); err != nil {
				continue
			}
			c.devices[did] = d.Clone()
			if rt := c.rt[did]; rt != nil {
				c.syncAlerts(d, rt, now.Unix())
			}
			n++
		}
		if c.winActive[id] != start.Unix() {
			c.winActive[id] = start.Unix()
			if n > 0 {
				evs = append(evs, ev{"Wartungsfenster begonnen", fmt.Sprintf("%s: %d %s bis %s in Wartung.", w.Name, n,
					plural1(n, "Gerät", "Geräte"), c.fmtTime(end.Unix()))})
			}
		}
	}
	c.mu.Unlock()
	for _, e := range evs {
		c.Event("info", "Wartungsfenster", e.title, e.body, nil, false)
	}
}
