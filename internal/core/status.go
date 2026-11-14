package core

import (
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/notify"
)

// reachKey is the synthesized reachability check.
const reachKey = "reach"

const msgNoData = "Keine aktuellen Daten"

// devState is the runtime state of one device.
type devState struct {
	st       model.DeviceStatus
	thr      map[string]string    // check key → threshold state after hysteresis (only bad ones)
	detail   map[string][2]string // check key → value and limit text of the threshold verdict
	fails    int                  // consecutive failed polls
	lastEval int64                // agents: time of the newest evaluated report
	saved    int64                // agents: LastSeen persisted with the device
	next     time.Time            // next scheduled poll
	running  bool                 // a scheduled poll is in progress
	dirty    bool                 // status changed since it was persisted
	pollMu   sync.Mutex           // serializes polls of this device
	offer    updateOffer          // agents: the last update offered
	newKeys  map[string]bool      // keys of new (undecided) checks, from st.Inventory
	// acceptUnseen makes the next collection the new inventory baseline: keys it reports for
	// the first time are accepted and keys it misses are dropped (the device's configuration
	// or the central service selection changed, so the difference is expected).
	acceptUnseen bool
}

// updateOffer remembers an update offered to an agent, so an agent that keeps reporting its
// old version (failed update, or a binary that reports another version) is not sent the same
// update with every report.
type updateOffer struct {
	sha  string // SHA-256 of the offered binary
	from string // version the agent reported when it was offered
	at   int64
}

func newDevState(id string) *devState {
	return &devState{
		st:  model.DeviceStatus{DeviceID: id, State: model.StateUnknown, Checks: []model.CheckStatus{}},
		thr: map[string]string{}, detail: map[string][2]string{}, newKeys: map[string]bool{},
	}
}

// openAlert is an active alert with the time of its last notification.
type openAlert struct {
	a        model.Alert
	notified int64
	peak     string                // worst state notified while open: gates the "Wieder OK" notification
	rules    map[string]*ruleState // progress of each notification rule (by rule ID)
}

func alertKey(deviceID, checkKey string) string { return deviceID + "\x00" + checkKey }

func cloneStatus(s model.DeviceStatus) model.DeviceStatus {
	s.Checks = slices.Clone(s.Checks)
	if s.Checks == nil {
		s.Checks = []model.CheckStatus{}
	}
	s.Hidden = slices.Clone(s.Hidden)
	s.Vanished = slices.Clone(s.Vanished)
	s.Inventory = slices.Clone(s.Inventory)
	return s
}

func reachCheck(state, msg string) model.Check {
	return model.Check{Key: reachKey, Name: "Erreichbarkeit", Group: "Status", State: state, Message: msg}
}

// staleStatus marks a check as without current data (poll failed, agent silent).
func staleStatus(cs model.CheckStatus, now int64) model.CheckStatus {
	if cs.State != model.StateUnknown {
		cs.Since = now
	}
	cs.State, cs.Native, cs.Message, cs.Reason, cs.Pending, cs.Muted = model.StateUnknown, model.StateUnknown, msgNoData, "", 0, ""
	return cs
}

// orderChecks puts reach first and network checks last, keeping the order otherwise.
func orderChecks(list []model.CheckStatus) []model.CheckStatus {
	rank := func(k string) int {
		switch {
		case k == reachKey:
			return 0
		case strings.HasPrefix(k, "net:"):
			return 2
		}
		return 1
	}
	slices.SortStableFunc(list, func(a, b model.CheckStatus) int { return rank(a.Key) - rank(b.Key) })
	return list
}

// deviceState is the worst state of all checks that are not unknown (unknown only when no
// check has data) and a short German reason.
// devState is the state a check contributes to its device: an acknowledged problem counts as ok.
func devStateOf(cs model.CheckStatus) string {
	if cs.Acked {
		return model.StateOK
	}
	return cs.State
}

// deviceState is the worst state of the device's checks (separate checks left out) and a
// short text about the problems.
func deviceState(all []model.CheckStatus) (string, string) {
	checks := make([]model.CheckStatus, 0, len(all))
	for _, cs := range all {
		if !model.SeparateCheck(cs.Key) {
			cs.State = devStateOf(cs)
			checks = append(checks, cs)
		}
	}
	worst := ""
	for _, cs := range checks {
		if cs.State == model.StateUnknown {
			continue
		}
		if worst == "" {
			worst = cs.State // Worst ranks "" like unknown, so seed with the first real state
		} else {
			worst = model.Worst(worst, cs.State)
		}
	}
	if worst == "" || worst == model.StateUnknown {
		for _, cs := range checks {
			if cs.Message != "" {
				return model.StateUnknown, cs.Message
			}
		}
		return model.StateUnknown, "Keine Daten"
	}
	if worst == model.StateOK {
		return model.StateOK, ""
	}
	var bad []model.CheckStatus
	for _, cs := range checks {
		if cs.State == model.StateWarn || cs.State == model.StateCrit {
			bad = append(bad, cs)
		}
	}
	slices.SortStableFunc(bad, func(a, b model.CheckStatus) int { return model.Rank(b.State) - model.Rank(a.State) })
	if len(bad) == 1 {
		return worst, problemText(bad[0])
	}
	parts := make([]string, 0, 3)
	for i, cs := range bad {
		if i == 3 {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, problemText(cs))
	}
	return worst, fmt.Sprintf("%d Probleme: %s", len(bad), strings.Join(parts, "; "))
}

func checkReason(cs model.CheckStatus) string {
	if cs.Reason != "" {
		return cs.Reason
	}
	return cs.Message
}

func problemText(cs model.CheckStatus) string {
	r := checkReason(cs)
	switch {
	case cs.Key == reachKey && r != "":
		return r
	case r == "":
		return cs.Name
	case r == cs.Name || strings.HasPrefix(r, cs.Name+" ") || strings.HasPrefix(r, cs.Name+":"):
		return r // a threshold reason often starts with the check name already ("Magenta Toner 7 % ≤ 10 %")
	}
	return cs.Name + ": " + r
}

// finish stores new checks of a device, derives the device state and syncs the alerts.
// Caller holds c.mu.
func (c *Core) finish(d model.Device, rt *devState, checks []model.CheckStatus, now int64) {
	checks = orderChecks(checks)
	keep := map[string]bool{}
	for _, cs := range checks {
		keep[cs.Key] = true
	}
	for k := range rt.thr {
		if !keep[k] {
			delete(rt.thr, k)
		}
	}
	for k := range rt.detail {
		if !keep[k] {
			delete(rt.detail, k)
		}
	}
	rt.st.DeviceID, rt.st.Checks = d.ID, checks
	rt.dirty = true
	c.syncAlerts(d, rt, now)
	// after the alerts: an acknowledged problem does not colour the device
	for i := range rt.st.Checks {
		cs := &rt.st.Checks[i]
		oa := c.open[alertKey(d.ID, cs.Key)]
		cs.Acked = oa != nil && oa.a.AckedBy != "" && (cs.State == model.StateWarn || cs.State == model.StateCrit)
	}
	state, msg := deviceState(rt.st.Checks)
	if state != rt.st.State || rt.st.Since == 0 {
		rt.st.Since = now
	}
	rt.st.State, rt.st.Message = state, msg
}

// ---- alerts ----

func sevName(state string) string {
	if state == model.StateCrit {
		return "critical"
	}
	return "warning"
}

func stateWord(state string) string {
	switch state {
	case model.StateCrit:
		return "Kritisch"
	case model.StateWarn:
		return "Warnung"
	case model.StateOK:
		return "OK"
	}
	return "Unbekannt"
}

// alertDetails are the facts of an alert notification. Caller holds c.mu.
func (c *Core) alertDetails(d model.Device, rt *devState, cs model.CheckStatus, status string) *notify.Details {
	dev := c.displayName(d)
	if d.Host != "" && d.Host != d.Name {
		dev += " (" + d.Host + ")"
	}
	facts := []notify.Fact{{Label: "Gerät", Value: dev}, {Label: "Check", Value: cs.Name}}
	if det, ok := rt.detail[cs.Key]; ok && cs.State != model.StateOK {
		facts = append(facts, notify.Fact{Label: "Wert", Value: det[0]}, notify.Fact{Label: "Schwelle", Value: det[1]})
	} else if r := checkReason(cs); r != "" {
		facts = append(facts, notify.Fact{Label: "Wert", Value: r})
	}
	if cs.Since > 0 {
		facts = append(facts, notify.Fact{Label: "seit", Value: c.fmtTime(cs.Since)})
	}
	return &notify.Details{Status: status, Facts: facts, Link: "/devices/" + d.ID}
}

// muteReason tells why a device raises no alerts ("" = it does).
func muteReason(d model.Device, now int64) string {
	switch {
	case !d.Enabled:
		return "Gerät deaktiviert"
	case d.NoAlerts:
		return "Alarme für das Gerät ausgeschaltet"
	case d.InMaintenance(now):
		return "Gerät in Wartung"
	}
	return ""
}

// syncAlerts opens, escalates and closes the alerts of a device to match its checks.
// Caller holds c.mu.
func (c *Core) syncAlerts(d model.Device, rt *devState, now int64) {
	muted := muteReason(d, now)
	name := c.displayName(d) // "<parent> / <node>" for an attached agent
	byKey := map[string]model.CheckStatus{}
	for _, cs := range rt.st.Checks {
		byKey[cs.Key] = cs
	}
	if muted == "" {
		for _, cs := range rt.st.Checks {
			if (cs.State != model.StateWarn && cs.State != model.StateCrit) || cs.Mode != "" {
				continue
			}
			k := alertKey(d.ID, cs.Key)
			title := fmt.Sprintf("%s: %s – %s", stateWord(cs.State), name, cs.Name)
			body := checkReason(cs)
			oa := c.open[k]
			if oa == nil && cs.Key == reachKey && cs.State == model.StateWarn {
				// One failed poll: shown, but only the second one in a row (crit) alerts,
				// otherwise every lost UDP packet sends "Warnung" and "Wieder OK".
				continue
			}
			if oa == nil {
				a := model.Alert{DeviceID: d.ID, DeviceName: name, CheckKey: cs.Key, CheckName: cs.Name,
					Severity: cs.State, Message: body, Started: now}
				id, err := c.St.OpenAlert(a)
				if err != nil {
					log.Printf("open alert: %v", err)
					continue
				}
				a.ID = id
				oa = &openAlert{a: a, notified: now, peak: cs.State}
				c.open[k] = oa
				det := c.alertDetails(d, rt, cs, "")
				c.Event(sevName(cs.State), name, title, body, det, false)
				c.notifyAlert(oa, d, c.message(sevName(cs.State), name, title, body, det), sevName(cs.State), false)
				continue
			}
			if oa.a.Severity == cs.State {
				continue
			}
			escalate := model.Rank(cs.State) > model.Rank(oa.a.Severity)
			if err := c.St.UpdateAlert(oa.a.ID, cs.State, body); err != nil {
				log.Printf("update alert: %v", err)
				continue
			}
			oa.a.Severity, oa.a.Message = cs.State, body
			if escalate {
				// An acknowledgement holds until the alert gets worse: then it counts (and
				// reminds) again.
				if oa.a.AckedBy != "" {
					if err := c.St.UnackAlert(oa.a.ID); err != nil {
						log.Printf("unack alert: %v", err)
					} else {
						oa.a.AckedBy, oa.a.AckedAt = "", 0
					}
				}
				oa.notified, oa.peak = now, model.Worst(oa.peak, cs.State)
				det := c.alertDetails(d, rt, cs, "")
				c.Event(sevName(cs.State), name, title, body, det, false)
				c.notifyAlert(oa, d, c.message(sevName(cs.State), name, title, body, det), sevName(cs.State), false)
			} else {
				c.Event("info", name, title, body, c.alertDetails(d, rt, cs, ""), false)
			}
		}
	}
	for k, oa := range c.open {
		if oa.a.DeviceID != d.ID {
			continue
		}
		cs, exists := byKey[oa.a.CheckKey]
		quiet := exists && cs.Mode != "" // quiet or new: never alerts
		if muted == "" && exists && !quiet && (cs.State == model.StateWarn || cs.State == model.StateCrit || cs.State == model.StateUnknown) {
			continue // still bad, or no current data: keep the alert as it is
		}
		if err := c.St.CloseAlert(oa.a.ID, now); err != nil {
			log.Printf("close alert: %v", err)
			continue
		}
		delete(c.open, k)
		if muted == "" && exists && !quiet {
			title := fmt.Sprintf("Wieder OK: %s – %s", name, oa.a.CheckName)
			body := cs.Message
			if body == "" {
				body = "Der Check ist wieder in Ordnung."
			}
			// Gate by the worst state that was notified: who got "Kritisch" also gets
			// "Wieder OK", even when the alert went back to warn before it closed.
			det := c.alertDetails(d, rt, cs, "ok")
			c.Event("info", name, title, body, det, false)
			c.notifyAlert(oa, d, c.message("info", name, title, body, det), sevName(model.Worst(oa.peak, oa.a.Severity)), true)
			continue
		}
		why := muted
		switch {
		case why != "":
		case quiet && cs.Mode == model.ModeNew:
			why = "Neuer Dienst, noch nicht übernommen."
		case quiet:
			why = "Der Check wird nicht mehr alarmiert (nur anzeigen)."
		case d.CheckMode(oa.a.CheckKey) == model.ModeOff:
			why = "Der Check wurde ausgeblendet."
		default:
			why = "Check entfernt"
		}
		c.Event("info", name, fmt.Sprintf("Alarm beendet: %s – %s", name, oa.a.CheckName), why, nil, false)
	}
}

// closeDeviceAlerts ends all alerts of a device silently. Caller holds c.mu.
func (c *Core) closeDeviceAlerts(deviceID string, now int64) {
	for k, oa := range c.open {
		if oa.a.DeviceID != deviceID {
			continue
		}
		if err := c.St.CloseAlert(oa.a.ID, now); err != nil {
			log.Printf("close alert: %v", err)
		}
		delete(c.open, k)
	}
}

// syncAllAlerts re-checks every device (maintenance that ended, re-enabled devices).
func (c *Core) syncAllAlerts() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now().Unix()
	for id, d := range c.devices {
		if rt := c.rt[id]; rt != nil {
			c.syncAlerts(d, rt, now)
		}
	}
}

// renotify repeats the notification of open, unacknowledged alerts every RenotifyHours.
func (c *Core) renotify() {
	hours := c.Settings().General.RenotifyHours
	if hours <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.nrules) > 0 {
		return // the rules remind (notifyTick)
	}
	now := c.Now().Unix()
	for _, oa := range c.open {
		d, ok := c.devices[oa.a.DeviceID]
		if !ok || oa.a.AckedBy != "" || muteReason(d, now) != "" || now-oa.notified < int64(hours)*3600 {
			continue
		}
		oa.notified = now
		rt := c.rt[d.ID]
		var cs model.CheckStatus
		for _, x := range rt.st.Checks {
			if x.Key == oa.a.CheckKey {
				cs = x
			}
		}
		if cs.Key == "" {
			continue
		}
		name := c.displayName(d)
		title := fmt.Sprintf("Weiterhin %s: %s – %s", stateWord(oa.a.Severity), name, oa.a.CheckName)
		c.send(c.message(sevName(oa.a.Severity), name, title, oa.a.Message, c.alertDetails(d, rt, cs, "")), sevName(oa.a.Severity))
	}
}

// checkSilence marks agents that stopped reporting: reach crit, other checks unknown.
func (c *Core) checkSilence() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now().Unix()
	// The check runs every 30 s. A much larger gap means this server was suspended or
	// paused (VM/container freeze): no report could arrive meanwhile, so restart the grace
	// period instead of declaring every agent silent at once.
	if last := c.silenceTick.Swap(now); last > 0 && now-last > 120 {
		c.runStart.Store(now)
		return
	}
	for id, d := range c.devices {
		if d.Type != model.TypeAgent || !d.Enabled {
			continue
		}
		rt := c.rt[id]
		ref := d.Created
		if d.Agent != nil && d.Agent.LastSeen > 0 {
			ref = d.Agent.LastSeen
		}
		limit := int64(max(3*d.PollInterval(), 180))
		// While the server was down no report could arrive: give the agents the full
		// period after the start before they count as silent.
		if ref == 0 || now-max(ref, c.runStart.Load()) <= limit {
			continue
		}
		msg := "Agent meldet sich nicht (seit " + c.fmtTime(ref) + ")"
		if d.Agent == nil || d.Agent.LastSeen == 0 {
			msg = "Agent hat sich noch nicht gemeldet (registriert " + c.fmtTime(ref) + ")"
		}
		prev := map[string]model.CheckStatus{}
		for _, cs := range rt.st.Checks {
			prev[cs.Key] = cs
		}
		if p, ok := prev[reachKey]; ok && p.State == model.StateCrit && p.Message == msg {
			continue
		}
		checks := []model.CheckStatus{c.evaluate(d, rt, prev, reachCheck(model.StateCrit, msg), now, nil)}
		for _, cs := range rt.st.Checks {
			switch {
			case cs.Key == reachKey:
			case strings.HasPrefix(cs.Key, "net:"):
				checks = append(checks, cs)
			default:
				checks = append(checks, staleStatus(cs, now))
			}
		}
		c.finish(d, rt, checks, now)
	}
}
