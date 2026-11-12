package core

import (
	"fmt"
	"slices"
	"strings"

	"pk95monitoring/internal/model"
)

// Check rules (model.Device.Rules) choose per check what is monitored: alert (default),
// quiet (collected, stored and shown, but never warn/crit and never alerts) or off (the
// check and its metrics are dropped; it is listed in DeviceStatus.Hidden).

const (
	maxRules     = 200
	maxRuleMatch = 200
)

// applyMode sets the state of an evaluated check for its mode. The verdict before the mode
// (State, or Muted for a check that was quiet) is kept, so switching between quiet and
// alert never needs a new poll.
func applyMode(cs model.CheckStatus, mode string) model.CheckStatus {
	eff := cs.State
	if cs.Muted != "" {
		eff = cs.Muted
	}
	cs.State, cs.Mode, cs.Muted = eff, "", ""
	if mode != model.ModeQuiet || cs.Key == reachKey {
		return cs
	}
	cs.Mode = model.ModeQuiet
	if eff == model.StateWarn || eff == model.StateCrit {
		cs.State, cs.Muted = model.StateOK, eff
	}
	return cs
}

// verdict is the state of a check before its mode was applied.
func verdict(cs model.CheckStatus) string {
	if cs.Muted != "" {
		return cs.Muted
	}
	return cs.State
}

// splitOff removes the checks a rule switches off and returns them as hidden entries.
func splitOff(d model.Device, checks []model.Check) ([]model.Check, []model.HiddenCheck) {
	if len(d.Rules) == 0 {
		return checks, nil
	}
	kept := make([]model.Check, 0, len(checks))
	var hidden []model.HiddenCheck
	for _, ch := range checks {
		if d.CheckMode(ch.Key) == model.ModeOff {
			hidden = append(hidden, model.HiddenCheck{Key: ch.Key, Name: ch.Name, Group: ch.Group})
			continue
		}
		kept = append(kept, ch)
	}
	return kept, hidden
}

// Scopes of the hidden list: each source replaces only its own entries (an agent's report
// its checks, a poll the network checks).
func scopeAll(string) bool       { return true }
func scopeNet(key string) bool   { return strings.HasPrefix(key, "net:") }
func scopeOther(key string) bool { return !scopeNet(key) }

// replaceHidden replaces the entries of one scope with fresh ones; the result is sorted by
// group, name and key and nil when empty.
func replaceHidden(old, fresh []model.HiddenCheck, scope func(string) bool) []model.HiddenCheck {
	out := make([]model.HiddenCheck, 0, len(old)+len(fresh))
	seen := map[string]bool{}
	for _, h := range fresh {
		if !seen[h.Key] {
			seen[h.Key] = true
			out = append(out, h)
		}
	}
	for _, h := range old {
		if !scope(h.Key) && !seen[h.Key] {
			seen[h.Key] = true
			out = append(out, h)
		}
	}
	return sortHidden(out)
}

func sortHidden(list []model.HiddenCheck) []model.HiddenCheck {
	if len(list) == 0 {
		return nil
	}
	slices.SortFunc(list, func(a, b model.HiddenCheck) int {
		if r := strings.Compare(a.Group, b.Group); r != 0 {
			return r
		}
		if r := strings.Compare(a.Name, b.Name); r != 0 {
			return r
		}
		return strings.Compare(a.Key, b.Key)
	})
	return list
}

// reapplyRules applies the rules of d to the current checks without a poll: checks switched
// off move to Hidden, the others get their quiet/alert state from the stored verdict.
// Hidden entries that are no longer switched off are dropped; those checks come back with
// the next poll or report. Caller holds c.mu and calls finish with the result.
func reapplyRules(d model.Device, rt *devState, checks []model.CheckStatus, now int64) []model.CheckStatus {
	out := make([]model.CheckStatus, 0, len(checks))
	var hidden []model.HiddenCheck
	seen := map[string]bool{}
	for _, cs := range checks {
		mode := d.CheckMode(cs.Key)
		if mode == model.ModeOff {
			hidden = append(hidden, model.HiddenCheck{Key: cs.Key, Name: cs.Name, Group: cs.Group})
			seen[cs.Key] = true
			continue
		}
		before := cs.State
		cs = applyMode(cs, mode)
		if cs.State != before {
			cs.Since = now
		}
		out = append(out, cs)
	}
	for _, h := range rt.st.Hidden {
		if !seen[h.Key] && d.CheckMode(h.Key) == model.ModeOff {
			seen[h.Key] = true
			hidden = append(hidden, h)
		}
	}
	rt.st.Hidden = sortHidden(hidden)
	return out
}

// onlyReach reports whether a rule match can only hit the reach check (no other check key
// starts with "r").
func onlyReach(match string) bool {
	return match == reachKey || (strings.HasPrefix(match, "r") && model.GlobMatch(match, reachKey))
}

// validateRules normalizes the rules of a device; errors are keyed "rules.N.field".
func validateRules(list []model.CheckRule, errs map[string]string) {
	if len(list) > maxRules {
		errs["rules"] = fmt.Sprintf("Höchstens %d Regeln pro Gerät.", maxRules)
	}
	seen := map[string]int{}
	for i := range list {
		r := &list[i]
		key := func(f string) string { return fmt.Sprintf("rules.%d.%s", i, f) }
		r.Match = strings.TrimSpace(r.Match)
		switch {
		case r.Match == "":
			errs[key("match")] = "Check oder Muster angeben."
		case len([]rune(r.Match)) > maxRuleMatch:
			errs[key("match")] = fmt.Sprintf("Höchstens %d Zeichen.", maxRuleMatch)
		case strings.ContainsAny(r.Match, "\n\r\t"):
			errs[key("match")] = "Keine Zeilenumbrüche oder Tabulatoren."
		case onlyReach(r.Match):
			errs[key("match")] = "Erreichbarkeit lässt sich nicht abschalten (dafür „Keine Alarme“ am Gerät)."
		}
		if j, dup := seen[r.Match]; dup && r.Match != "" {
			errs[key("match")] = fmt.Sprintf("Doppelt (wie Zeile %d).", j+1)
		} else if r.Match != "" {
			seen[r.Match] = i
		}
		if r.Mode == "" {
			r.Mode = model.ModeAlert
		}
		if !slices.Contains([]string{model.ModeAlert, model.ModeQuiet, model.ModeOff}, r.Mode) {
			errs[key("mode")] = "Überwachen, Nur anzeigen oder Ausblenden auswählen."
		}
	}
}
