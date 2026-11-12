package fake

import (
	"context"
	"hash/fnv"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/collect/netcheck"
	"pk95monitoring/internal/core"
	"pk95monitoring/internal/model"
)

// backfillStep is the time between back-filled samples in seconds (tests make it longer).
var backfillStep int64 = 300

// demoPolled are the polled demo devices (back-filled from one poll).
var demoPolled = []string{"demo-pve", "demo-switch", "demo-unifi", "demo-ups", "demo-nut", "demo-printer", "demo-nas", "demo-web"}

// backfill writes BackfillDays of 5-minute samples for every demo device that has no
// history yet (all of them on the first start; on later starts the demo devices added by a
// newer version), then rolls them up. Agents use their simulation; the other devices are
// polled once and their values are varied around the current ones.
func backfill(c *core.Core, agents []*simAgent, now time.Time) error {
	if BackfillDays <= 0 {
		return nil
	}
	fresh := func(id string) bool {
		list, err := c.St.SeriesList(id)
		return err == nil && len(list) == 0
	}
	end := now.Unix() - now.Unix()%backfillStep
	start := end - int64(BackfillDays)*86400
	wrote := false

	for _, a := range agents {
		d, err := c.Device(a.id)
		if err != nil || d.Agent == nil || !fresh(a.id) {
			continue
		}
		wrote = true
		for ts := start; ts < end; ts += backfillStep {
			if err := c.St.WriteMetrics(a.id, ts, visible(d, a.checks(c, ts, d.Agent.Services))); err != nil {
				return err
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var wg sync.WaitGroup
	templates := map[string][]model.Check{}
	var mu sync.Mutex
	for _, id := range demoPolled {
		d, err := c.Device(id)
		if err != nil || !fresh(id) {
			continue
		}
		wg.Go(func() {
			var checks []model.Check
			switch d.Type {
			case model.TypeProxmox:
				checks, _ = c.Proxmox.Collect(ctx, d)
			case model.TypeSNMP:
				checks, _ = c.SNMP.Collect(ctx, d)
			case model.TypeNUT:
				checks, _ = c.NUT.Collect(ctx, d)
			}
			for _, nc := range d.Checks {
				checks = append(checks, netcheck.Run(ctx, nc, d.Host))
			}
			mu.Lock()
			templates[id] = addRates(visible(d, checks))
			mu.Unlock()
		})
	}
	wg.Wait()
	for id, tpl := range templates {
		for ts := start; ts < end; ts += backfillStep {
			if err := c.St.WriteMetrics(id, ts, vary(tpl, ts, end)); err != nil {
				return err
			}
			wrote = true
		}
	}
	if !wrote {
		return nil
	}
	var done int64
	if err := c.St.GetSetting(keyBackfill, &done); err == nil && done == 0 {
		if err := c.St.SetSetting(keyBackfill, now.Unix()); err != nil {
			return err
		}
	}
	return c.St.Rollup(now.Unix())
}

// visible drops the checks a rule of the device switches off (they store no metrics).
func visible(d model.Device, checks []model.Check) []model.Check {
	return slices.DeleteFunc(checks, func(ch model.Check) bool { return d.CheckMode(ch.Key) == model.ModeOff })
}

// addRates adds the rate metrics a first poll cannot have yet (counters need two polls).
func addRates(checks []model.Check) []model.Check {
	for i := range checks {
		ch := &checks[i]
		has := map[string]bool{}
		var guestInst string
		for _, m := range ch.Metrics {
			has[m.Name] = true
			if m.Name == "guest.cpu" {
				guestInst = m.Instance
			}
		}
		switch {
		case guestInst != "" && !has["guest.netin"]:
			s := seedOf(ch.Key)
			for j, n := range []string{"guest.netin", "guest.netout", "guest.diskread", "guest.diskwrite"} {
				ch.Metrics = append(ch.Metrics, model.Metric{Name: n, Instance: guestInst, Unit: "B/s",
					Value: 20e3 + 400e3*noise(s+float64(j), 0)})
			}
		case strings.HasPrefix(ch.Key, "snmp:if:") && strings.HasPrefix(ch.Message, "Verbunden") && !has["if.rx"]: // ports that are up only
			inst, s := strings.TrimPrefix(ch.Key, "snmp:if:"), seedOf(ch.Key)
			ch.Metrics = append(ch.Metrics,
				model.Metric{Name: "if.rx", Instance: inst, Unit: "B/s", Value: 5e3 + 2e6*noise(s, 1)},
				model.Metric{Name: "if.tx", Instance: inst, Unit: "B/s", Value: 5e3 + 1.5e6*noise(s, 2)},
				model.Metric{Name: "if.errors", Instance: inst, Unit: "1/s", Value: 0})
		}
	}
	return checks
}

func seedOf(s string) float64 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return float64(h.Sum32()%10000) / 100
}

// vary returns the template checks with values for time ts (end = now): gauges swing with
// the day, counters and ages run backwards.
func vary(tpl []model.Check, ts, end int64) []model.Check {
	out := make([]model.Check, 0, len(tpl))
	ago := float64(end - ts)
	day := 2 * math.Pi * float64(ts%86400) / 86400
	for _, ch := range tpl {
		if len(ch.Metrics) == 0 {
			continue
		}
		n := model.Check{Key: ch.Key, Name: ch.Name, Group: ch.Group, State: ch.State}
		for _, m := range ch.Metrics {
			s := seedOf(ch.Key + m.SeriesKey())
			w := 0.6*math.Sin(day-2+s) + 0.25*math.Sin(float64(ts)/1900+s) + 0.3*(noise(s, ts)-0.5)
			v := m.Value
			switch {
			case strings.HasSuffix(m.Name, ".total"):
			case m.Name == "uptime":
				if v -= ago; v < 0 {
					continue
				}
			case m.Name == "printer.pages":
				v = math.Max(math.Round(v-ago/3600*4), 0)
			case m.Name == "printer.toner":
				v = math.Min(v+ago/86400*0.4, 100)
			case m.Name == "cert.days":
				v += ago / 86400
			case m.Unit == "%":
				v = clamp(v*(1+0.25*w), 0, 100)
				if strings.HasPrefix(m.Name, "ups.battery") || strings.HasSuffix(m.Name, "storage.usage") || m.Name == "disk.usage" {
					v = clamp(m.Value+0.5*w, 0, 100)
				}
			case m.Unit == "B/s" || m.Unit == "1/s" || m.Unit == "ms":
				v = math.Max(v*(1+0.6*w), 0)
			case m.Unit == "B" || m.Unit == "min":
				v = math.Max(v*(1+0.04*w), 0)
			case m.Unit == "°C" || m.Unit == "V":
				v += 1.5 * w
			case m.Unit == "W":
				v = math.Max(v*(1+0.15*w), 0)
			case m.Unit == "rpm":
				v = math.Max(v*(1+0.05*w), 0)
			}
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				m.Value = math.Round(v*100) / 100
				n.Metrics = append(n.Metrics, m)
			}
		}
		out = append(out, n)
	}
	return out
}
