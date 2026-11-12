package proxmox

import (
	"context"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

var rateNames = [4]string{"guest.netin", "guest.netout", "guest.diskread", "guest.diskwrite"}

// minRateInterval is the shortest interval a rate is computed over. PVE refreshes the guest
// counters about every 10 s, so a poll right after another one (manual "poll now") would
// see unchanged counters and report a false 0 B/s; such a poll keeps the older sample.
const minRateInterval = 10 * time.Second

// guestKind maps a resource type to the key prefix, label, group and API path.
func guestKind(typ string) (kind, label, group, stopped string) {
	if typ == "lxc" {
		return "ct", "CT", "Container", "Container läuft nicht"
	}
	return "vm", "VM", "VMs", "VM läuft nicht"
}

func (c *Collector) guestChecks(ctx context.Context, cl *client, d model.Device, inv inventory, now time.Time) []model.Check {
	checks := make([]model.Check, 0, len(inv.guests))
	seen := map[string]bool{}
	for _, g := range inv.guests {
		id := g.vmid()
		kind, label, group, stoppedMsg := guestKind(g.Type)
		inst := kind + "/" + id
		name := strings.TrimSpace(label + " " + id + " " + g.Name)
		ch := model.Check{Key: "pve:" + kind + ":" + id, Name: name, Group: group, Facts: map[string]string{"Knoten": g.Node}}
		if g.MaxCPU > 0 {
			ch.Facts["vCPUs"] = strconv.Itoa(int(g.MaxCPU))
		}
		if g.MaxMem > 0 {
			ch.Facts["RAM"] = fmtBytes(float64(g.MaxMem))
		}
		switch {
		case !inv.online[g.Node]:
			ch.State, ch.Message = model.StateUnknown, "Knoten "+g.Node+" offline"
		case g.Status == "running":
			seen[inst] = true
			memPct := pct(float64(g.Mem), float64(g.MaxMem))
			ch.State = model.StateOK
			ch.Message = "läuft auf " + g.Node
			if g.Uptime > 0 {
				ch.Message += " " + fmtSince(float64(g.Uptime))
			}
			ch.Message += " · CPU " + fmtPct(float64(g.CPU)*100) + " · RAM " + fmtPct(memPct)
			ch.Metrics = []model.Metric{
				{Name: "guest.cpu", Instance: inst, Value: round2(float64(g.CPU) * 100), Unit: "%"},
				{Name: "guest.mem", Instance: inst, Value: round2(memPct), Unit: "%"},
				{Name: "guest.mem_used", Instance: inst, Value: float64(g.Mem), Unit: "B"},
			}
			ch.Metrics = append(ch.Metrics, c.rates(d.ID, inst, g, now)...)
		default:
			watched, err := c.watched(ctx, cl, d, g, now)
			state := strings.TrimSpace(g.Status)
			switch {
			case err != nil:
				ch.State, ch.Message = model.StateUnknown, "gestoppt – Autostart unbekannt: "+permMessage(err, "VM.Audit")
			case watched:
				ch.State, ch.Message = model.StateCrit, stoppedMsg
				if state != "stopped" && state != "" {
					ch.Message += " (Status " + state + ")"
				}
			default:
				ch.State, ch.Message = model.StateOK, "gestoppt"
				if state != "stopped" && state != "" {
					ch.Message = "Status " + state
				}
			}
		}
		checks = append(checks, ch)
	}
	// Drop counters of guests that stopped, moved away or were deleted.
	c.mu.Lock()
	for inst := range c.dev(d.ID).counters {
		if !seen[inst] {
			delete(c.dev(d.ID).counters, inst)
		}
	}
	c.mu.Unlock()
	return checks
}

// watched reports whether a stopped guest must be running (cfg.WatchGuests minus IgnoreGuests).
func (c *Collector) watched(ctx context.Context, cl *client, d model.Device, g resource, now time.Time) (bool, error) {
	id := g.vmid()
	for _, ig := range d.Proxmox.IgnoreGuests {
		if strings.TrimSpace(ig) == id {
			return false, nil
		}
	}
	switch strings.ToLower(strings.TrimSpace(d.Proxmox.WatchGuests)) {
	case "all":
		return true, nil
	case "none":
		return false, nil
	}
	key := g.Type + "/" + id
	c.mu.Lock()
	e, ok := c.dev(d.ID).onboot[key]
	c.mu.Unlock()
	if ok && now.Sub(e.at) < configTTL {
		return e.on, nil
	}
	on, err := cl.onboot(ctx, g.Node, g.Type, id)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	c.dev(d.ID).onboot[key] = onbootEntry{on: on, at: now}
	c.mu.Unlock()
	return on, nil
}

// rates turns the cumulative guest counters into B/s. The first sample and counters that
// went down (guest restarted) yield no rate.
func (c *Collector) rates(devID, inst string, g resource, now time.Time) []model.Metric {
	cur := [4]float64{float64(g.NetIn), float64(g.NetOut), float64(g.DiskRead), float64(g.DiskWrite)}
	c.mu.Lock()
	st := c.dev(devID)
	prev, ok := st.counters[inst]
	elapsed := now.Sub(prev.at)
	short := ok && elapsed >= 0 && elapsed < minRateInterval
	if short && !counterDecreased(cur, prev.v) {
		c.mu.Unlock()
		return nil // keep the older sample as the base of the next rate
	}
	st.counters[inst] = counterSample{at: now, v: cur}
	c.mu.Unlock()
	if !ok || short || elapsed <= 0 {
		return nil
	}
	dt := elapsed.Seconds()
	var out []model.Metric
	for i, name := range rateNames {
		if cur[i] < prev.v[i] {
			continue
		}
		out = append(out, model.Metric{Name: name, Instance: inst, Value: round2((cur[i] - prev.v[i]) / dt), Unit: "B/s"})
	}
	return out
}

func counterDecreased(cur, prev [4]float64) bool {
	for i := range cur {
		if cur[i] < prev[i] {
			return true
		}
	}
	return false
}

// storageChecks reports every storage per node; a shared storage (NFS, Ceph …) only once,
// on the first node where it is available.
func storageChecks(inv inventory) []model.Check {
	sharedNode := map[string]string{}
	for _, s := range inv.storages { // sorted by storage, node
		if s.Shared != 1 {
			continue
		}
		cur, ok := sharedNode[s.Storage]
		if !ok || (s.Status == "available" && inv.online[s.Node] && !storageUp(inv, cur, s.Storage)) {
			sharedNode[s.Storage] = s.Node
		}
	}
	var checks []model.Check
	for _, s := range inv.storages {
		if s.Shared == 1 && sharedNode[s.Storage] != s.Node {
			continue
		}
		ch := model.Check{
			Key:   "pve:storage:" + s.Node + ":" + s.Storage,
			Name:  "Speicher " + s.Storage + " (" + s.Node + ")",
			Group: "Speicher",
			Facts: map[string]string{"Typ": s.PluginType},
		}
		if s.Shared == 1 {
			ch.Facts["Gemeinsam"] = "ja"
		}
		inst := s.Node + "/" + s.Storage
		switch {
		case !inv.online[s.Node]:
			ch.State, ch.Message = model.StateUnknown, "Knoten "+s.Node+" offline"
		case s.Status != "available":
			ch.State, ch.Message = model.StateCrit, "Speicher nicht verfügbar"
			if s.Status != "" {
				ch.Message += " (Status " + s.Status + ")"
			}
		default:
			ch.State = model.StateOK
			used, total := float64(s.Disk), float64(s.MaxDisk)
			if total > 0 {
				p := pct(used, total)
				ch.Message = fmtPct(p) + " belegt (" + fmtBytes(used) + " von " + fmtBytes(total) + ")"
				ch.Metrics = []model.Metric{
					{Name: "storage.usage", Instance: inst, Value: round2(p), Unit: "%"},
					{Name: "storage.used", Instance: inst, Value: used, Unit: "B"},
					{Name: "storage.total", Instance: inst, Value: total, Unit: "B"},
				}
				ch.Facts["Größe"] = fmtBytes(total)
			} else {
				ch.Message = "verfügbar"
			}
		}
		checks = append(checks, ch)
	}
	return checks
}

func storageUp(inv inventory, node, storage string) bool {
	if !inv.online[node] {
		return false
	}
	for _, s := range inv.storages {
		if s.Node == node && s.Storage == storage {
			return s.Status == "available"
		}
	}
	return false
}
