package core

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/notify"
	"pk95monitoring/internal/store"
)

// Notification rules decide who gets which alert notification. Without rules every alert goes
// to all enabled channels (filtered by Notify.MinSeverity, repeated every RenotifyHours). With
// rules, each enabled rule that matches an alert (device or folder, kind of check, severity)
// adds its channels; one notification goes to the union. A rule can remind while the alert is
// open and unacknowledged, escalate it to other recipients after some minutes and hold its
// notifications during a quiet time (e.g. at night), summarised in one message at its end.
// System messages (no alert) keep going to all channels.

// KindNotifyRule is the store kind of the notification rules.
const KindNotifyRule = "notifyrule"

// keyDigests: rule ID → end (unix) of the last quiet time that was summarised.
const keyDigests = "notify_digests"

// Kinds of checks a rule can pick.
const (
	NotifyReach   = "reach"   // Erreichbarkeit
	NotifyChecks  = "checks"  // all other checks of the device
	NotifyUpdates = "updates" // updates, pve:updates:<node>
	NotifyEvents  = "events"  // Windows event rules
)

var notifyKinds = []string{NotifyReach, NotifyChecks, NotifyUpdates, NotifyEvents}

// digestMaxAge: a quiet time that ended longer ago is not summarised any more (server down).
const digestMaxAge = 6 * time.Hour

// NotifyRule is one notification rule.
type NotifyRule struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	Devices []string `json:"devices"` // empty with Folders empty = all devices
	Folders []string `json:"folders"` // with subfolders
	Kinds   []string `json:"kinds"`   // reach, checks, updates, events; empty = all
	// MinSeverity: warn (warnings and critical) or crit (only critical).
	MinSeverity string `json:"min_severity"`
	Mail        bool   `json:"mail"`
	MailTo      string `json:"mail_to"` // comma separated; empty = the recipients of the mail settings
	Telegram    bool   `json:"telegram"`
	Hook        bool   `json:"hook"`
	// RepeatMinutes reminds while the alert is open and not acknowledged (0 = never).
	RepeatMinutes int `json:"repeat_minutes"`
	// EscalateMinutes: an alert not acknowledged after this long goes to the escalation
	// recipients once (0 = no escalation).
	EscalateMinutes  int    `json:"escalate_minutes"`
	EscalateMailTo   string `json:"escalate_mail_to"`
	EscalateTelegram bool   `json:"escalate_telegram"`
	EscalateHook     bool   `json:"escalate_hook"`
	// Quiet time "22:00"–"07:00" (empty = none), starting on QuietDays (0 = Sunday; empty =
	// every day). Notifications are held, critical ones too unless QuietCritical; with Digest
	// the alerts of the quiet time are summarised at its end.
	QuietStart    string `json:"quiet_start"`
	QuietEnd      string `json:"quiet_end"`
	QuietDays     []int  `json:"quiet_days"`
	QuietCritical bool   `json:"quiet_critical"`
	Digest        bool   `json:"digest"`
	Created       int64  `json:"created"`
	Updated       int64  `json:"updated"`
}

// NotifyRuleView is a rule for the UI.
type NotifyRuleView struct {
	NotifyRule
	Matches  int    `json:"matches"`   // devices it applies to
	Quiet    string `json:"quiet"`     // German description of the quiet time, "" = none
	QuietNow bool   `json:"quiet_now"` // the quiet time is running
}

// ruleState is the progress of one rule for one open alert.
type ruleState struct {
	sent      bool  // the alert was notified (or summarised) to the rule's channels
	escalated bool  // the escalation went out
	last      int64 // last notification or reminder
}

// target is where a notification goes.
type target struct {
	mail           []string
	telegram, hook bool
}

func (t *target) add(o target) {
	for _, m := range o.mail {
		if !slices.ContainsFunc(t.mail, func(x string) bool { return strings.EqualFold(x, m) }) {
			t.mail = append(t.mail, m)
		}
	}
	t.telegram = t.telegram || o.telegram
	t.hook = t.hook || o.hook
}

func (t target) empty() bool { return len(t.mail) == 0 && !t.telegram && !t.hook }

// alertKind is the kind of check a rule picks by.
func alertKind(key string) string {
	switch {
	case key == reachKey:
		return NotifyReach
	case key == "updates" || strings.HasPrefix(key, "pve:updates:"):
		return NotifyUpdates
	case key == "eventlog" || strings.HasPrefix(key, "ev:"):
		return NotifyEvents
	}
	return NotifyChecks
}

func atLeast(state, min string) bool { return model.Rank(state) >= model.Rank(min) }

// quietOn returns the quiet time starting on the day of t.
func (r NotifyRule) quietOn(t time.Time) (time.Time, time.Time, bool) {
	sh, sm, ok1 := parseClock(r.QuietStart)
	eh, em, ok2 := parseClock(r.QuietEnd)
	if !ok1 || !ok2 || r.QuietStart == r.QuietEnd {
		return time.Time{}, time.Time{}, false
	}
	if len(r.QuietDays) > 0 && !slices.Contains(r.QuietDays, int(t.Weekday())) {
		return time.Time{}, time.Time{}, false
	}
	y, mo, d := t.Date()
	from := time.Date(y, mo, d, sh, sm, 0, 0, t.Location())
	to := time.Date(y, mo, d, eh, em, 0, 0, t.Location())
	if !to.After(from) {
		to = to.AddDate(0, 0, 1) // over midnight
	}
	return from, to, true
}

// quietSpan returns the quiet time that contains t, if any.
func (r NotifyRule) quietSpan(t time.Time) (time.Time, time.Time, bool) {
	for _, d := range []int{0, -1} {
		from, to, ok := r.quietOn(t.AddDate(0, 0, d))
		if ok && !t.Before(from) && t.Before(to) {
			return from, to, true
		}
	}
	return time.Time{}, time.Time{}, false
}

// lastQuiet returns the latest quiet time that ended at or before t (within the last 8 days).
func (r NotifyRule) lastQuiet(t time.Time) (time.Time, time.Time, bool) {
	for d := 0; d >= -8; d-- {
		from, to, ok := r.quietOn(t.AddDate(0, 0, d))
		if ok && !to.After(t) {
			return from, to, true
		}
	}
	return time.Time{}, time.Time{}, false
}

// holds reports whether a notification of that severity is held at t (quiet time).
func (r NotifyRule) holds(t time.Time, state string) bool {
	if r.QuietCritical && state == model.StateCrit {
		return false
	}
	_, _, ok := r.quietSpan(t)
	return ok
}

// describeQuiet describes the quiet time in German ("22:00–07:00 Uhr, beginnt Mo, Di"), ""
// without one.
func (r NotifyRule) describeQuiet() string {
	if _, _, ok := parseClock(r.QuietStart); !ok || r.QuietStart == r.QuietEnd {
		return ""
	}
	s := r.QuietStart + "–" + r.QuietEnd + " Uhr"
	if len(r.QuietDays) > 0 && len(r.QuietDays) < 7 {
		names := []string{"So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"}
		days := slices.Clone(r.QuietDays)
		slices.SortFunc(days, func(a, b int) int { return cmp.Compare((a+6)%7, (b+6)%7) }) // Monday first
		var ds []string
		for _, d := range days {
			ds = append(ds, names[d])
		}
		s += ", beginnt " + strings.Join(ds, ", ")
	}
	return s
}

// ruleMatches reports whether a rule applies to a check of device d (directly, by folder or
// through its parent). Caller holds c.mu.
func (c *Core) ruleMatches(r NotifyRule, d model.Device, key string) bool {
	if len(r.Kinds) > 0 && !slices.Contains(r.Kinds, alertKind(key)) {
		return false
	}
	if len(r.Devices) == 0 && len(r.Folders) == 0 {
		return true
	}
	return c.windowMatches(MaintWindow{Devices: r.Devices, Folders: r.Folders}, d)
}

// ruleTarget is where the notifications of a rule go; escalation = the escalation targets.
func (c *Core) ruleTarget(r NotifyRule, escalation bool) target {
	if escalation {
		return target{mail: notify.Recipients(r.EscalateMailTo), telegram: r.EscalateTelegram, hook: r.EscalateHook}
	}
	t := target{telegram: r.Telegram, hook: r.Hook}
	if r.Mail {
		t.mail = notify.Recipients(r.MailTo)
		if len(t.mail) == 0 {
			t.mail = notify.Recipients(c.Settings().Notify.MailTo)
		}
	}
	return t
}

// enabledRules lists the enabled rules (by ID). Caller holds c.mu.
func (c *Core) enabledRules() []NotifyRule {
	var out []NotifyRule
	for _, r := range c.nrules {
		if r.Enabled {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b NotifyRule) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// state returns the progress of a rule for the alert; fresh = it was just created.
func (oa *openAlert) state(rule string) (*ruleState, bool) {
	if oa.rules == nil {
		oa.rules = map[string]*ruleState{}
	}
	st, ok := oa.rules[rule]
	if !ok {
		st = &ruleState{}
		oa.rules[rule] = st
	}
	return st, !ok
}

// notifyAlert sends the notification of an alert that opened or got worse (ok = false) or
// ended (ok = true) to the matching rules, or – without rules – to all channels when gate
// passes the severity filter. Caller holds c.mu.
func (c *Core) notifyAlert(oa *openAlert, d model.Device, m notify.Message, gate string, ok bool) {
	if len(c.nrules) == 0 {
		c.send(m, gate)
		return
	}
	now := c.Now()
	var t target
	for _, r := range c.enabledRules() {
		if !c.ruleMatches(r, d, oa.a.CheckKey) {
			continue
		}
		st, _ := oa.state(r.ID)
		if ok {
			// "Wieder OK" to who got the alert, unless the quiet time holds it (summary)
			if st.sent && !r.holds(now, model.Worst(oa.peak, oa.a.Severity)) {
				t.add(c.ruleTarget(r, false))
			}
			continue
		}
		if !atLeast(oa.a.Severity, r.MinSeverity) || r.holds(now, oa.a.Severity) {
			continue
		}
		st.sent, st.last = true, now.Unix()
		t.add(c.ruleTarget(r, false))
	}
	c.sendTarget(m, t)
}

// notifyTick sends reminders and escalations of the open alerts and the summaries of quiet
// times that just ended. Called every 30 s.
func (c *Core) notifyTick() {
	c.mu.Lock()
	if len(c.nrules) == 0 {
		c.mu.Unlock()
		return
	}
	now := c.Now()
	rules := c.enabledRules()
	for _, oa := range c.open {
		d, ok := c.devices[oa.a.DeviceID]
		if !ok || muteReason(d, now.Unix()) != "" {
			continue
		}
		var remind, esc target
		for _, r := range rules {
			if !c.ruleMatches(r, d, oa.a.CheckKey) || !atLeast(oa.a.Severity, r.MinSeverity) {
				continue
			}
			st, fresh := oa.state(r.ID)
			if fresh {
				// the alert is older than the rule (or this server's start): counts as notified
				st.sent, st.last = true, now.Unix()
				st.escalated = r.EscalateMinutes > 0 && now.Unix()-oa.a.Started >= int64(r.EscalateMinutes)*60
				continue
			}
			if oa.a.AckedBy != "" || r.holds(now, oa.a.Severity) {
				continue
			}
			if !st.sent { // held during the quiet time
				st.sent, st.last = true, now.Unix()
				if !r.Digest {
					remind.add(c.ruleTarget(r, false))
				}
				continue
			}
			if r.EscalateMinutes > 0 && !st.escalated && now.Unix()-oa.a.Started >= int64(r.EscalateMinutes)*60 {
				st.escalated = true
				esc.add(c.ruleTarget(r, true))
			}
			if r.RepeatMinutes > 0 && now.Unix()-st.last >= int64(r.RepeatMinutes)*60 {
				st.last = now.Unix()
				remind.add(c.ruleTarget(r, false))
			}
		}
		if remind.empty() && esc.empty() {
			continue
		}
		rt := c.rt[d.ID]
		cs := model.CheckStatus{Check: model.Check{Key: oa.a.CheckKey, Name: oa.a.CheckName, State: oa.a.Severity, Message: oa.a.Message}}
		if rt != nil {
			for _, x := range rt.st.Checks {
				if x.Key == oa.a.CheckKey {
					cs = x
				}
			}
		} else {
			rt = &devState{}
		}
		name := c.displayName(d)
		sev := sevName(oa.a.Severity)
		if !remind.empty() {
			title := fmt.Sprintf("Weiterhin %s: %s – %s", stateWord(oa.a.Severity), name, oa.a.CheckName)
			c.sendTarget(c.message(sev, name, title, oa.a.Message, c.alertDetails(d, rt, cs, "")), remind)
		}
		if !esc.empty() {
			title := fmt.Sprintf("Eskalation: %s – %s seit %s nicht quittiert", name, oa.a.CheckName, durText(now.Unix()-oa.a.Started))
			det := c.alertDetails(d, rt, cs, "")
			det.Note = "Diese Meldung geht an die Eskalation, weil der Alarm nicht rechtzeitig quittiert wurde."
			c.sendTarget(c.message(sev, name, title, oa.a.Message, det), esc)
		}
	}
	type due struct {
		r        NotifyRule
		from, to time.Time
	}
	var digests []due
	for _, r := range rules {
		if !r.Digest {
			continue
		}
		from, to, ok := r.lastQuiet(now)
		if !ok || to.Unix() <= c.digests[r.ID] || to.Unix() <= r.Created || now.Sub(to) > digestMaxAge {
			continue
		}
		c.digests[r.ID] = to.Unix()
		digests = append(digests, due{r, from, to})
	}
	if len(digests) > 0 {
		if err := c.St.SetSetting(keyDigests, c.digests); err != nil {
			log.Printf("save digests: %v", err)
		}
	}
	c.mu.Unlock()
	for _, x := range digests {
		c.sendDigest(x.r, x.from, x.to)
	}
}

// sendDigest summarises the alerts of a rule that started in the quiet time [from, to): what
// happened and whether it is fixed. Nothing is sent when there were none.
func (c *Core) sendDigest(r NotifyRule, from, to time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alerts, err := c.St.AlertsBetween(ctx, from.Unix(), to.Unix())
	if err != nil {
		log.Printf("digest %s: %v", r.Name, err)
		return
	}
	now := c.Now().Unix()
	c.mu.Lock()
	var rows []notify.Row
	open := 0
	for _, a := range alerts {
		d, ok := c.devices[a.DeviceID]
		if !ok || a.Started < from.Unix() || !atLeast(a.Severity, r.MinSeverity) || !c.ruleMatches(r, d, a.CheckKey) {
			continue
		}
		row := notify.Row{Kind: stateWord(a.Severity), ID: a.DeviceName, Name: a.CheckName, Time: a.Started}
		if a.Ended > 0 {
			row.Status, row.Duration = "ok", durText(a.Ended-a.Started)
			row.Info = "Behoben um " + time.Unix(a.Ended, 0).Format("15:04") + " Uhr"
			if a.Ended-from.Unix() > 86400 { // the time of day alone would be ambiguous
				row.Info = "Behoben am " + time.Unix(a.Ended, 0).Format("02.01. um 15:04") + " Uhr"
			}
		} else {
			open++
			row.Status, row.Duration = "warning", "seit "+durText(now-a.Started)
			if a.Severity == model.StateCrit {
				row.Status = "error"
			}
			row.Info = "Noch offen – " + a.Message
			if a.AckedBy != "" {
				row.Info = "Noch offen, quittiert von " + a.AckedBy + " – " + a.Message
			}
		}
		rows = append(rows, row)
	}
	t := c.ruleTarget(r, false)
	c.mu.Unlock()
	if len(rows) == 0 || t.empty() {
		return
	}
	slices.SortStableFunc(rows, func(a, b notify.Row) int { return cmp.Compare(a.Time, b.Time) })
	span := from.Format("15:04") + "–" + to.Format("15:04") + " Uhr"
	title := "Ruhezeit " + span + ": " + plural(len(rows), "Alarm", "Alarme")
	body := "Alles ist wieder behoben."
	sev, status := "info", "ok"
	if open > 0 {
		title += fmt.Sprintf(", %d noch offen", open)
		body = fmt.Sprintf("%d von %d Problemen sind noch nicht behoben.", open, len(rows))
		if len(rows) == 1 {
			body = "Das Problem ist noch nicht behoben."
		}
		sev, status = "warning", "warning"
	}
	det := &notify.Details{Status: status, Rows: rows, Link: "/alerts",
		RowsTitle:  "Alarme während der Ruhezeit (" + from.Format("02.01. 15:04") + " bis " + to.Format("02.01. 15:04") + ")",
		CountWords: [3]string{"behoben", "Warnung offen", "kritisch offen"},
		Facts:      []notify.Fact{{Label: "Regel", Value: r.Name}}}
	c.Event("info", "Benachrichtigung", "Zusammenfassung der Ruhezeit gesendet", r.Name+": "+title+".", nil, false)
	c.sendTarget(c.message(sev, "Benachrichtigung", title, body, det), t)
}

// sendTarget delivers m to the channels of t that are switched on in the notify settings.
func (c *Core) sendTarget(m notify.Message, t target) {
	if t.empty() {
		return
	}
	ns := c.Settings().Notify
	ns.MailEnabled = ns.MailEnabled && len(t.mail) > 0
	ns.MailTo = strings.Join(t.mail, ", ")
	ns.TelegramEnabled = ns.TelegramEnabled && t.telegram
	ns.HookEnabled = ns.HookEnabled && t.hook
	if !channelsOn(ns) {
		return
	}
	c.deliver(ns, m)
}

// ---- rules: list, save, delete ----

// NotifyRules lists the rules by name.
func (c *Core) NotifyRules() []NotifyRuleView {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now()
	out := make([]NotifyRuleView, 0, len(c.nrules))
	for _, r := range c.nrules {
		out = append(out, c.ruleView(r, now))
	}
	slices.SortFunc(out, func(a, b NotifyRuleView) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), strings.Compare(a.ID, b.ID))
	})
	return out
}

// ruleView builds the API view of a rule. Caller holds c.mu.
func (c *Core) ruleView(r NotifyRule, now time.Time) NotifyRuleView {
	v := NotifyRuleView{NotifyRule: r, Quiet: r.describeQuiet()}
	_, _, v.QuietNow = r.quietSpan(now)
	for _, d := range c.devices {
		if c.attached(d) {
			continue // counted with its parent
		}
		if len(r.Devices) == 0 && len(r.Folders) == 0 || c.windowMatches(MaintWindow{Devices: r.Devices, Folders: r.Folders}, d) {
			v.Matches++
		}
	}
	return v
}

// validateRule normalises a rule and returns German field errors.
func validateRule(r *NotifyRule) map[string]string {
	errs := map[string]string{}
	r.Name, r.MailTo, r.EscalateMailTo = strings.TrimSpace(r.Name), strings.TrimSpace(r.MailTo), strings.TrimSpace(r.EscalateMailTo)
	r.QuietStart, r.QuietEnd = strings.TrimSpace(r.QuietStart), strings.TrimSpace(r.QuietEnd)
	r.Folders, r.Devices = cleanList(r.Folders), cleanList(r.Devices)
	if r.MinSeverity == "" {
		r.MinSeverity = model.StateWarn
	}
	switch {
	case r.Name == "":
		errs["name"] = "Namen angeben."
	case len([]rune(r.Name)) > 80:
		errs["name"] = "Höchstens 80 Zeichen."
	}
	kinds := []string{}
	for _, k := range r.Kinds {
		if !slices.Contains(notifyKinds, k) {
			errs["kinds"] = "Ungültige Art."
		} else if !slices.Contains(kinds, k) {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) == len(notifyKinds) {
		kinds = []string{}
	}
	r.Kinds = kinds
	if r.MinSeverity != model.StateWarn && r.MinSeverity != model.StateCrit {
		errs["min_severity"] = "Warnung oder kritisch."
	}
	if len(r.Devices) > 10000 || len(r.Folders) > 1000 {
		errs["targets"] = "Zu viele Geräte oder Ordner."
	}
	mails := func(key, list string) {
		if len(list) > 2000 {
			errs[key] = "Zu viele Empfänger."
			return
		}
		for _, a := range notify.Recipients(list) {
			if !strings.Contains(a, "@") || strings.ContainsAny(a, " \t\r\n<>") || len(a) > 254 {
				errs[key] = "Ungültige Adresse: " + a
				return
			}
		}
	}
	mails("mail_to", r.MailTo)
	mails("escalate_mail_to", r.EscalateMailTo)
	if !r.Mail && !r.Telegram && !r.Hook {
		errs["channels"] = "Mindestens einen Weg wählen: E-Mail, Telegram oder Webhook."
	}
	if r.RepeatMinutes < 0 || r.RepeatMinutes > 10080 {
		errs["repeat_minutes"] = "0 (nie) bis 10080 Minuten."
	}
	if r.EscalateMinutes < 0 || r.EscalateMinutes > 10080 {
		errs["escalate_minutes"] = "0 (keine) bis 10080 Minuten."
	}
	if r.EscalateMinutes > 0 && r.EscalateMailTo == "" && !r.EscalateTelegram && !r.EscalateHook {
		errs["escalate_mail_to"] = "Empfänger der Eskalation angeben (E-Mail, Telegram oder Webhook)."
	}
	if r.EscalateMinutes == 0 {
		r.EscalateMailTo, r.EscalateTelegram, r.EscalateHook = "", false, false
	}
	if r.QuietStart != "" || r.QuietEnd != "" {
		sh, sm, ok1 := parseClock(r.QuietStart)
		eh, em, ok2 := parseClock(r.QuietEnd)
		switch {
		case !ok1:
			errs["quiet_start"] = "Uhrzeit wie 22:00 angeben."
		case !ok2:
			errs["quiet_end"] = "Uhrzeit wie 07:00 angeben."
		case sh == eh && sm == em:
			errs["quiet_end"] = "Ende und Beginn müssen sich unterscheiden."
		default:
			r.QuietStart, r.QuietEnd = fmt.Sprintf("%02d:%02d", sh, sm), fmt.Sprintf("%02d:%02d", eh, em)
		}
	} else {
		r.QuietDays, r.QuietCritical, r.Digest = nil, false, false
	}
	days := []int{}
	for _, d := range r.QuietDays {
		if d < 0 || d > 6 {
			errs["quiet_days"] = "Ungültiger Wochentag."
		} else if !slices.Contains(days, d) {
			days = append(days, d)
		}
	}
	if len(days) == 7 {
		days = []int{}
	}
	slices.Sort(days)
	r.QuietDays = days
	return errs
}

// SaveNotifyRule creates (empty ID) or changes a rule.
func (c *Core) SaveNotifyRule(r NotifyRule) (NotifyRuleView, map[string]string, error) {
	c.mu.Lock()
	now := c.Now()
	if r.ID != "" {
		old, ok := c.nrules[r.ID]
		if !ok {
			c.mu.Unlock()
			return NotifyRuleView{}, nil, store.ErrNotFound
		}
		r.Created = old.Created
	}
	errs := validateRule(&r)
	for _, id := range r.Devices {
		if _, ok := c.devices[id]; !ok {
			errs["targets"] = "Unbekanntes Gerät."
		}
	}
	if len(errs) > 0 {
		c.mu.Unlock()
		return NotifyRuleView{}, errs, nil
	}
	created := r.ID == ""
	if created {
		if len(c.nrules) >= 100 {
			c.mu.Unlock()
			return NotifyRuleView{}, map[string]string{"name": "Höchstens 100 Regeln."}, nil
		}
		r.ID, r.Created = newID(8), now.Unix()
	}
	r.Updated = now.Unix()
	if err := c.St.Put(KindNotifyRule, r.ID, r); err != nil {
		c.mu.Unlock()
		return NotifyRuleView{}, nil, err
	}
	first := len(c.nrules) == 0
	c.nrules[r.ID] = r
	v := c.ruleView(r, now)
	c.mu.Unlock()
	title := "Benachrichtigungsregel geändert"
	if created {
		title = "Benachrichtigungsregel angelegt"
	}
	body := r.Name + "."
	if first {
		body += " Ab jetzt entscheiden die Regeln, wer welche Alarme bekommt."
	}
	c.Event("info", "Benachrichtigung", title, body, nil, false)
	return v, nil, nil
}

// DeleteNotifyRule removes a rule.
func (c *Core) DeleteNotifyRule(id string) error {
	c.mu.Lock()
	r, ok := c.nrules[id]
	if !ok {
		c.mu.Unlock()
		return store.ErrNotFound
	}
	if err := c.St.Delete(KindNotifyRule, id); err != nil {
		c.mu.Unlock()
		return err
	}
	delete(c.nrules, id)
	delete(c.digests, id)
	for _, oa := range c.open {
		delete(oa.rules, id)
	}
	last := len(c.nrules) == 0
	c.mu.Unlock()
	body := r.Name + "."
	if last {
		body += " Ohne Regeln gehen alle Alarme wieder an alle Benachrichtigungswege."
	}
	c.Event("info", "Benachrichtigung", "Benachrichtigungsregel gelöscht", body, nil, false)
	return nil
}
