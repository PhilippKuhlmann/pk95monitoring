package core

import (
	"fmt"
	"slices"
	"strings"

	"pk95monitoring/internal/eventrules"
	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

// Discovery (inventory): the first successful collection of a device records every check
// key it reports. Keys reported later are new (undecided, ModeNew: shown and stored, never
// alerting) until a rule matches them or they are accepted. Keys of the inventory missing
// from vanishAfter successful collections in a row are vanished until they come back or are
// removed. The synthetic check "discovery" (Dienste-Inventur) is warn while there is
// something to decide, so nothing appears or disappears unnoticed.

const (
	vanishAfter   = 2    // successful collections in a row without a key before it counts as vanished
	maxInventory  = 5000 // keys kept per device
	discoveryName = "Dienste-Inventur"
	maxNamesInMsg = 3
)

// inventoryKey reports whether a check takes part in the inventory: not reach, not the
// discovery check, not network checks and system log rules (both are configured), not the
// event log check of agents before 0.16 (replaced by the rules) and not the last backup per
// guest (follows the guests, which the inventory has already).
func inventoryKey(k string) bool {
	return k != "" && k != reachKey && k != model.DiscoveryKey && k != "eventlog" && !strings.HasPrefix(k, "net:") && !strings.HasPrefix(k, eventrules.KeyPrefix) &&
		!strings.HasPrefix(k, "pve:bak:") && !strings.HasPrefix(k, "hv:") // Hyper-V VMs move (cluster, Replica failover)
}

// reindex rebuilds the set of new keys and the vanished list from the inventory.
func (rt *devState) reindex() {
	rt.newKeys = map[string]bool{}
	for _, it := range rt.st.Inventory {
		if it.New {
			rt.newKeys[it.Key] = true
		}
	}
	rt.st.Vanished = vanishedOf(rt.st.Inventory)
}

func vanishedOf(inv []model.InventoryItem) []model.VanishedCheck {
	var out []model.VanishedCheck
	for _, it := range inv {
		if it.Missing >= vanishAfter {
			out = append(out, model.VanishedCheck{Key: it.Key, Name: it.Name, Group: it.Group, Last: it.Last})
		}
	}
	return out
}

// modeOf is the effective mode of a check: a matching rule decides; without one a new
// (undecided) check runs in ModeNew, every other check is monitored.
func modeOf(d model.Device, rt *devState, key string) string {
	if rt != nil && rt.newKeys[key] && !d.HasRule(key) {
		return model.ModeNew
	}
	return d.CheckMode(key)
}

// discover merges a successful collection (all checks, before rules switch any off) into
// the inventory of d. Caller holds c.mu.
func discover(d model.Device, rt *devState, checks []model.Check, now int64) {
	seen := map[string]model.Check{}
	for _, ch := range checks {
		if inventoryKey(ch.Key) {
			seen[ch.Key] = ch
		}
	}
	first := len(rt.st.Inventory) == 0
	if first && len(seen) == 0 {
		return
	}
	decided := func(key string) bool { return d.AutoAccept || d.HasRule(key) }
	inv := make([]model.InventoryItem, 0, len(rt.st.Inventory)+len(seen))
	have := map[string]bool{}
	for _, it := range rt.st.Inventory {
		if !inventoryKey(it.Key) {
			continue // stored by an older version
		}
		if ch, ok := seen[it.Key]; ok {
			it.Name, it.Group, it.Last, it.Missing = ch.Name, ch.Group, now, 0
		} else if rt.acceptUnseen {
			continue // the selection changed: what is gone now was deselected, not lost
		} else {
			it.Missing++
		}
		if it.New && decided(it.Key) {
			it.New = false
		}
		// a new check that disappears again before anybody decided is forgotten
		if it.New && it.Missing >= vanishAfter {
			continue
		}
		have[it.Key] = true
		inv = append(inv, it)
	}
	for _, ch := range checks {
		if !inventoryKey(ch.Key) || have[ch.Key] {
			continue
		}
		have[ch.Key] = true
		inv = append(inv, model.InventoryItem{Key: ch.Key, Name: ch.Name, Group: ch.Group, First: now, Last: now,
			New: !first && !rt.acceptUnseen && !decided(ch.Key)})
	}
	rt.acceptUnseen = false
	if len(inv) > maxInventory {
		// keep the present keys: drop the ones missing longest
		slices.SortStableFunc(inv, func(a, b model.InventoryItem) int { return a.Missing - b.Missing })
		inv = inv[:maxInventory]
	}
	rt.st.Inventory = inv
	rt.reindex()
}

// acceptRuled clears the new mark of checks a rule (or AutoAccept) decides now. Caller
// holds c.mu.
func acceptRuled(d model.Device, rt *devState) {
	changed := false
	for i, it := range rt.st.Inventory {
		if it.New && (d.AutoAccept || d.HasRule(it.Key)) {
			rt.st.Inventory[i].New = false
			changed = true
		}
	}
	if changed {
		rt.reindex()
	}
}

// discoveryCheck is the synthetic inventory check; false while the device has no inventory.
func discoveryCheck(rt *devState) (model.Check, bool) {
	inv := rt.st.Inventory
	if len(inv) == 0 {
		return model.Check{}, false
	}
	var news, gone []string
	for _, it := range inv {
		switch {
		case it.New:
			news = append(news, it.Name)
		case it.Missing >= vanishAfter:
			gone = append(gone, it.Name)
		}
	}
	ch := model.Check{Key: model.DiscoveryKey, Name: discoveryName, Group: "Status", State: model.StateOK}
	var parts []string
	if len(news) > 0 {
		parts = append(parts, fmt.Sprintf("%s: %s", plural(len(news), "neuer Dienst", "neue Dienste"), nameList(news)))
	}
	if len(gone) > 0 {
		parts = append(parts, fmt.Sprintf("%s verschwunden: %s", plural(len(gone), "Dienst", "Dienste"), nameList(gone)))
	}
	if len(parts) > 0 {
		ch.State, ch.Message = model.StateWarn, strings.Join(parts, "; ")
	} else {
		ch.Message = fmt.Sprintf("%s bekannt, nichts zu entscheiden", plural(len(inv), "Dienst", "Dienste"))
	}
	return ch, true
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// nameList joins the first names ("A, B, C und 4 weitere").
func nameList(names []string) string {
	if len(names) <= maxNamesInMsg {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s und %d weitere", strings.Join(names[:maxNamesInMsg], ", "), len(names)-maxNamesInMsg)
}

// refreshDiscovery re-evaluates the discovery check in checks after the inventory changed
// without a collection (accept, remove, rules). Caller holds c.mu.
func (c *Core) refreshDiscovery(d model.Device, rt *devState, checks []model.CheckStatus, now int64) []model.CheckStatus {
	ch, ok := discoveryCheck(rt)
	i := slices.IndexFunc(checks, func(cs model.CheckStatus) bool { return cs.Key == model.DiscoveryKey })
	if !ok || i < 0 {
		return checks // switched off by a rule, or not collected yet: the next collection adds it
	}
	prev := map[string]model.CheckStatus{model.DiscoveryKey: checks[i]}
	checks = slices.Clone(checks)
	checks[i] = c.evaluate(d, rt, prev, ch, now, c.Settings().Thresholds)
	return checks
}

// Discovery accepts new checks (they are monitored from now on, by their rules) and removes
// vanished checks from the inventory (they count as new when they come back). Unknown keys
// are ignored. It returns the number of accepted and removed checks.
func (c *Core) Discovery(id string, accept, remove []string) (int, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.devices[id]
	rt := c.rt[id]
	if !ok || rt == nil {
		return 0, 0, store.ErrNotFound
	}
	acc, rm := map[string]bool{}, map[string]bool{}
	for _, k := range accept {
		acc[k] = true
	}
	for _, k := range remove {
		rm[k] = true
	}
	var nAcc, nRm int
	inv := make([]model.InventoryItem, 0, len(rt.st.Inventory))
	for _, it := range rt.st.Inventory {
		if rm[it.Key] && it.Missing >= vanishAfter {
			nRm++
			continue
		}
		if acc[it.Key] && it.New {
			it.New = false
			nAcc++
		}
		inv = append(inv, it)
	}
	if nAcc+nRm == 0 {
		return 0, 0, nil
	}
	rt.st.Inventory = inv
	rt.reindex()
	now := c.Now().Unix()
	checks := reapplyRules(d, rt, rt.st.Checks, now)
	c.finish(d, rt, c.refreshDiscovery(d, rt, checks, now), now)
	name := c.displayName(d)
	if nAcc > 0 {
		c.Event("info", name, "Neue Dienste übernommen", plural(nAcc, "Dienst wird", "Dienste werden")+" jetzt überwacht.", nil, false)
	}
	if nRm > 0 {
		c.Event("info", name, "Verschwundene Dienste entfernt", plural(nRm, "Dienst", "Dienste")+" aus der Inventur entfernt.", nil, false)
	}
	return nAcc, nRm, nil
}
