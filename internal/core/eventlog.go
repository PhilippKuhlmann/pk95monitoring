package core

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"pk95monitoring/internal/eventrules"
	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

// System logs (Windows event log, Linux journal): central rules (the built-in ones of
// package eventrules, changed or not, plus own rules) live in Settings.Agent; each agent may switch central rules off, add its
// own rules and exceptions (model.AgentConfig). The agent evaluates the rules and reports
// one check "ev:<rule>" per rule with the latest matching events.

// EventRuleView is a central event rule for the UI.
type EventRuleView struct {
	model.EventRule
	Builtin bool   `json:"builtin"`
	Changed bool   `json:"changed"` // a built-in rule that differs from its default
	Filter  string `json:"filter"`  // German summary of the filters
	Trigger string `json:"trigger"` // when it alerts
	Off     int    `json:"off"`     // agents (of the rule's OS) that switched it off
}

// EventIgnoreView is an exception with the device it belongs to (none = all devices).
type EventIgnoreView struct {
	model.EventIgnore
	DeviceID string `json:"device_id,omitempty"`
	Device   string `json:"device,omitempty"`
}

// EventCheckRow is one event check of an agent.
type EventCheckRow struct {
	DeviceID string  `json:"device_id"`
	Device   string  `json:"device"`
	Group    string  `json:"group,omitempty"`
	CheckKey string  `json:"check_key"`
	Name     string  `json:"name"`
	State    string  `json:"state"`
	Acked    bool    `json:"acked,omitempty"`
	Mode     string  `json:"mode,omitempty"`
	Message  string  `json:"message"`
	Count    float64 `json:"count"`
	Last     int64   `json:"last,omitempty"` // time of the newest event
}

// EventItem is a reported event with its device and rule.
type EventItem struct {
	model.EventEntry
	DeviceID string `json:"device_id"`
	Device   string `json:"device"`
	CheckKey string `json:"check_key"`
	Rule     string `json:"rule"`
}

// EventOverview is the page of the system logs.
type EventOverview struct {
	Agents   int               `json:"agents"`  // enabled agents with system log rules
	Windows  int               `json:"windows"` // of them Windows
	Linux    int               `json:"linux"`   // of them Linux
	Rules    []EventRuleView   `json:"rules"`
	Ignore   []EventIgnoreView `json:"ignore"`
	Rows     []EventCheckRow   `json:"rows"`
	Events   []EventItem       `json:"events"`
	Defaults []model.EventRule `json:"defaults"`
}

// maxOverviewEvents limits the events listed on the overview.
const maxOverviewEvents = 200

// eventOS is the OS of an agent whose system log the rules read ("" = none).
func eventOS(d model.Device) string {
	if d.Type != model.TypeAgent || d.Agent == nil {
		return ""
	}
	switch d.Agent.OS {
	case eventrules.OSWindows, eventrules.OSLinux:
		return d.Agent.OS
	}
	return ""
}

// eventRuleViews lists the central rules. Caller holds c.mu (for the device counts).
func (c *Core) eventRuleViews() []EventRuleView {
	central := eventrules.Merge(c.Settings().Agent.EventRules)
	out := make([]EventRuleView, 0, len(central))
	for _, r := range central {
		v := EventRuleView{EventRule: r, Filter: eventrules.Describe(r), Trigger: eventrules.Trigger(r)}
		if def, ok := eventrules.Default(r.ID); ok {
			v.Builtin, v.Changed = true, !sameRule(def, r)
		}
		for _, d := range c.devices {
			if eventOS(d) == eventrules.RuleOS(r) && slices.Contains(d.Agent.EventOff, r.ID) {
				v.Off++
			}
		}
		out = append(out, v)
	}
	return out
}

// sameRule compares the settings of two rules (not the hint).
func sameRule(a, b model.EventRule) bool {
	return a.Name == b.Name && a.Enabled == b.Enabled && slices.Equal(a.Logs, b.Logs) && slices.Equal(a.Sources, b.Sources) &&
		slices.Equal(a.IDs, b.IDs) && slices.Equal(a.Levels, b.Levels) && a.Text == b.Text && a.Count == b.Count &&
		a.Hours == b.Hours && a.Severity == b.Severity
}

// EventLog returns the overview of the system logs: rules, exceptions, the event checks of
// all enabled Windows and Linux agents (worst first) and their latest events.
func (c *Core) EventLog() EventOverview {
	c.mu.Lock()
	defer c.mu.Unlock()
	ov := EventOverview{Rules: c.eventRuleViews(), Ignore: []EventIgnoreView{}, Rows: []EventCheckRow{}, Events: []EventItem{},
		Defaults: eventrules.Defaults()}
	for _, ig := range c.Settings().Agent.EventIgnore {
		ov.Ignore = append(ov.Ignore, EventIgnoreView{EventIgnore: ig})
	}
	seen := map[string]bool{}
	for id, d := range c.devices {
		os := eventOS(d)
		if os == "" {
			continue
		}
		name := c.displayName(d)
		for _, ig := range d.Agent.EventIgnore {
			ov.Ignore = append(ov.Ignore, EventIgnoreView{EventIgnore: ig, DeviceID: id, Device: name})
		}
		rt := c.rt[id]
		if rt == nil || !d.Enabled {
			continue
		}
		ov.Agents++
		if os == eventrules.OSLinux {
			ov.Linux++
		} else {
			ov.Windows++
		}
		group := d.Group
		if p, ok := c.parentOf(d); ok && group == "" {
			group = p.Group
		}
		for _, cs := range rt.st.Checks {
			if !strings.HasPrefix(cs.Key, eventrules.KeyPrefix) {
				continue
			}
			row := EventCheckRow{DeviceID: id, Device: name, Group: group, CheckKey: cs.Key, Name: cs.Name, State: cs.State,
				Acked: cs.Acked, Mode: cs.Mode, Message: cs.Message}
			for _, m := range cs.Metrics {
				if m.Name == eventrules.MetricCount {
					row.Count = m.Value
				}
			}
			for _, e := range cs.Events {
				row.Last = max(row.Last, e.Time)
				k := fmt.Sprintf("%s|%d|%s|%s|%d", id, e.Time, e.Log, e.Source, e.ID)
				if !seen[k] { // the same event may match several rules
					seen[k] = true
					ov.Events = append(ov.Events, EventItem{EventEntry: e, DeviceID: id, Device: name, CheckKey: cs.Key, Rule: cs.Name})
				}
			}
			ov.Rows = append(ov.Rows, row)
		}
	}
	slices.SortFunc(ov.Rows, func(a, b EventCheckRow) int {
		ra, rb := model.Rank(a.State), model.Rank(b.State)
		if a.Acked {
			ra = 0
		}
		if b.Acked {
			rb = 0
		}
		return cmp.Or(rb-ra, cmp.Compare(b.Count, a.Count), cmp.Compare(strings.ToLower(a.Device), strings.ToLower(b.Device)), strings.Compare(a.Name, b.Name))
	})
	slices.SortFunc(ov.Events, func(a, b EventItem) int {
		return cmp.Or(cmp.Compare(b.Time, a.Time), strings.Compare(a.Device, b.Device))
	})
	if len(ov.Events) > maxOverviewEvents {
		ov.Events = ov.Events[:maxOverviewEvents]
	}
	slices.SortStableFunc(ov.Ignore, func(a, b EventIgnoreView) int {
		return cmp.Or(cmp.Compare(a.Device, b.Device), cmp.Compare(a.Created, b.Created))
	})
	return ov
}

// updateAgentSettings changes the agent settings and stores them.
func (c *Core) updateAgentSettings(fn func(a *AgentSettings)) error {
	c.smu.Lock()
	defer c.smu.Unlock()
	s := c.settings
	s.Agent.EventRules = model.CloneEventRules(s.Agent.EventRules)
	s.Agent.EventIgnore = model.CloneEventIgnores(s.Agent.EventIgnore)
	fn(&s.Agent)
	if err := c.St.SetSetting(keySettings, s); err != nil {
		return err
	}
	c.settings = s
	return nil
}

// SaveEventRule creates (empty ID) or changes a central event rule. A built-in rule saved
// with its default settings is reset (its stored copy is dropped).
func (c *Core) SaveEventRule(r model.EventRule) (EventRuleView, map[string]string, error) {
	stored := c.Settings().Agent.EventRules
	builtin := eventrules.IsBuiltin(r.ID)
	idx := slices.IndexFunc(stored, func(s model.EventRule) bool { return s.ID == r.ID })
	switch {
	case r.ID == "":
		if len(stored) >= eventrules.MaxRules+len(eventrules.Defaults()) {
			return EventRuleView{}, map[string]string{"name": fmt.Sprintf("Höchstens %d eigene Regeln.", eventrules.MaxRules)}, nil
		}
		r.ID = newID(6)
		for eventrules.IsBuiltin(r.ID) {
			r.ID = newID(6)
		}
	case !builtin && idx < 0:
		return EventRuleView{}, nil, store.ErrNotFound
	}
	def, _ := eventrules.Default(r.ID)
	if builtin {
		r.Hint, r.OS = def.Hint, def.OS // hint and OS of a built-in rule are fixed
	} else if idx >= 0 {
		r.OS = stored[idx].OS
	}
	if errs := eventrules.Validate(&r, ""); len(errs) > 0 {
		return EventRuleView{}, errs, nil
	}
	err := c.updateAgentSettings(func(a *AgentSettings) {
		a.EventRules = slices.DeleteFunc(a.EventRules, func(s model.EventRule) bool { return s.ID == r.ID })
		if builtin && sameRule(def, r) {
			return
		}
		if idx >= 0 && idx <= len(a.EventRules) {
			a.EventRules = slices.Insert(a.EventRules, idx, r)
		} else {
			a.EventRules = append(a.EventRules, r)
		}
	})
	if err != nil {
		return EventRuleView{}, nil, err
	}
	c.Event("info", "Systemprotokolle", "Ereignisregel gespeichert", r.Name+": "+eventrules.Describe(r)+" – "+eventrules.Trigger(r)+".", nil, false)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, v := range c.eventRuleViews() {
		if v.ID == r.ID {
			return v, nil, nil
		}
	}
	return EventRuleView{}, nil, store.ErrNotFound
}

// DeleteEventRule deletes an own central rule or resets a built-in one to its default.
// Agents that switched the rule off keep that entry (harmless).
func (c *Core) DeleteEventRule(id string) error {
	stored := c.Settings().Agent.EventRules
	i := slices.IndexFunc(stored, func(s model.EventRule) bool { return s.ID == id })
	if i < 0 {
		if eventrules.IsBuiltin(id) {
			return nil // already the default
		}
		return store.ErrNotFound
	}
	name := stored[i].Name
	if err := c.updateAgentSettings(func(a *AgentSettings) {
		a.EventRules = slices.DeleteFunc(a.EventRules, func(s model.EventRule) bool { return s.ID == id })
	}); err != nil {
		return err
	}
	title := "Ereignisregel gelöscht"
	if eventrules.IsBuiltin(id) {
		title = "Ereignisregel zurückgesetzt"
	}
	c.Event("info", "Systemprotokolle", title, name+".", nil, false)
	return nil
}

// AddEventIgnore adds an exception for one agent (deviceID) or for all (empty deviceID).
func (c *Core) AddEventIgnore(deviceID string, ig model.EventIgnore) (EventIgnoreView, map[string]string, error) {
	if errs := eventrules.ValidateIgnore(&ig, ""); len(errs) > 0 {
		return EventIgnoreView{}, errs, nil
	}
	ig.ID, ig.Created = newID(6), c.Now().Unix()
	what := ignoreText(ig)
	if deviceID == "" {
		if len(c.Settings().Agent.EventIgnore) >= eventrules.MaxIgnores {
			return EventIgnoreView{}, map[string]string{"source": fmt.Sprintf("Höchstens %d Ausnahmen.", eventrules.MaxIgnores)}, nil
		}
		if err := c.updateAgentSettings(func(a *AgentSettings) { a.EventIgnore = append(a.EventIgnore, ig) }); err != nil {
			return EventIgnoreView{}, nil, err
		}
		c.Event("info", "Systemprotokolle", "Ausnahme für alle Geräte angelegt", what+".", nil, false)
		return EventIgnoreView{EventIgnore: ig}, nil, nil
	}
	c.mu.Lock()
	d, ok := c.devices[deviceID]
	if !ok || d.Type != model.TypeAgent || d.Agent == nil {
		c.mu.Unlock()
		return EventIgnoreView{}, nil, store.ErrNotFound
	}
	if len(d.Agent.EventIgnore) >= eventrules.MaxIgnores {
		c.mu.Unlock()
		return EventIgnoreView{}, map[string]string{"source": fmt.Sprintf("Höchstens %d Ausnahmen.", eventrules.MaxIgnores)}, nil
	}
	d = d.Clone()
	d.Agent.EventIgnore = append(d.Agent.EventIgnore, ig)
	if err := c.St.Put(KindDevice, d.ID, d); err != nil {
		c.mu.Unlock()
		return EventIgnoreView{}, nil, err
	}
	c.devices[d.ID] = d
	name := c.displayName(d)
	c.mu.Unlock()
	c.Event("info", "Systemprotokolle", "Ausnahme angelegt", name+": "+what+".", nil, false)
	return EventIgnoreView{EventIgnore: ig, DeviceID: d.ID, Device: name}, nil, nil
}

// DeleteEventIgnore removes an exception of an agent (deviceID) or a central one.
func (c *Core) DeleteEventIgnore(deviceID, id string) error {
	has := func(list []model.EventIgnore) bool {
		return slices.ContainsFunc(list, func(ig model.EventIgnore) bool { return ig.ID == id })
	}
	drop := func(list []model.EventIgnore) []model.EventIgnore {
		return slices.DeleteFunc(list, func(ig model.EventIgnore) bool { return ig.ID == id })
	}
	if deviceID == "" {
		if !has(c.Settings().Agent.EventIgnore) {
			return store.ErrNotFound
		}
		return c.updateAgentSettings(func(a *AgentSettings) { a.EventIgnore = drop(a.EventIgnore) })
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.devices[deviceID]
	if !ok || d.Agent == nil || !has(d.Agent.EventIgnore) {
		return store.ErrNotFound
	}
	d = d.Clone()
	d.Agent.EventIgnore = drop(d.Agent.EventIgnore)
	if err := c.St.Put(KindDevice, d.ID, d); err != nil {
		return err
	}
	c.devices[d.ID] = d
	return nil
}

// ignoreText describes an exception in German.
func ignoreText(ig model.EventIgnore) string {
	var parts []string
	if ig.Source != "" {
		parts = append(parts, "Quelle "+ig.Source)
	}
	if len(ig.IDs) > 0 {
		ids := make([]string, len(ig.IDs))
		for i, v := range ig.IDs {
			ids[i] = fmt.Sprint(v)
		}
		parts = append(parts, "ID "+strings.Join(ids, ", "))
	}
	if ig.Text != "" {
		parts = append(parts, "Text „"+ig.Text+"“")
	}
	return strings.Join(parts, " · ")
}

// validateEvents normalises event rules and exceptions (new IDs for missing, invalid or
// duplicate ones) and adds field errors keyed prefix + "event_rules.N.name" etc.
func validateEvents(rules *[]model.EventRule, ignore *[]model.EventIgnore, now int64, prefix string, errs map[string]string) {
	if *rules == nil {
		*rules = []model.EventRule{}
	}
	if *ignore == nil {
		*ignore = []model.EventIgnore{}
	}
	if len(*rules) > eventrules.MaxRules+len(eventrules.Defaults()) {
		errs[prefix+"event_rules"] = fmt.Sprintf("Höchstens %d Regeln.", eventrules.MaxRules)
	}
	ids := map[string]bool{}
	for i := range *rules {
		r := &(*rules)[i]
		if !eventrules.ValidID(r.ID) || ids[r.ID] {
			r.ID = newID(6)
		}
		ids[r.ID] = true
		for k, v := range eventrules.Validate(r, fmt.Sprintf("%sevent_rules.%d.", prefix, i)) {
			errs[k] = v
		}
	}
	if len(*ignore) > eventrules.MaxIgnores {
		errs[prefix+"event_ignore"] = fmt.Sprintf("Höchstens %d Ausnahmen.", eventrules.MaxIgnores)
	}
	ids = map[string]bool{}
	for i := range *ignore {
		ig := &(*ignore)[i]
		if !eventrules.ValidID(ig.ID) || ids[ig.ID] {
			ig.ID = newID(6)
		}
		ids[ig.ID] = true
		if ig.Created == 0 {
			ig.Created = now
		}
		for k, v := range eventrules.ValidateIgnore(ig, fmt.Sprintf("%sevent_ignore.%d.", prefix, i)) {
			errs[k] = v
		}
	}
}

// validateAgentEvents normalises the event settings of an agent ("agent.event_…" errors).
func validateAgentEvents(a *model.AgentConfig, now int64, errs map[string]string) {
	off := []string{}
	for _, id := range a.EventOff {
		if id = strings.TrimSpace(id); eventrules.ValidID(id) && !slices.Contains(off, id) {
			off = append(off, id)
		}
	}
	a.EventOff = off
	if len(a.EventRules) > eventrules.MaxRules {
		errs["agent.event_rules"] = fmt.Sprintf("Höchstens %d Regeln.", eventrules.MaxRules)
	}
	for i := range a.EventRules { // own rules read the log of the agent's OS
		a.EventRules[i].OS = ""
		if a.OS == eventrules.OSLinux {
			a.EventRules[i].OS = eventrules.OSLinux
		}
	}
	validateEvents(&a.EventRules, &a.EventIgnore, now, "agent.", errs)
}

// AgentEvents returns the event rules and exceptions of an agent (sent with every reply).
func (c *Core) AgentEvents(d model.Device) ([]model.EventRule, []model.EventIgnore) {
	os := eventOS(d)
	if os == "" {
		return []model.EventRule{}, []model.EventIgnore{}
	}
	set := c.Settings().Agent
	rules := eventrules.Effective(eventrules.ForOS(eventrules.Merge(set.EventRules), os), d.Agent.EventOff, eventrules.ForOS(d.Agent.EventRules, os))
	ignore := append(model.CloneEventIgnores(set.EventIgnore), model.CloneEventIgnores(d.Agent.EventIgnore)...)
	if ignore == nil {
		ignore = []model.EventIgnore{}
	}
	return rules, ignore
}
