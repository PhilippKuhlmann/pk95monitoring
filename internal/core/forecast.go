package core

import (
	"log"
	"maps"
	"math"
	"slices"
	"time"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

// Storage forecast: once an hour the fill level history of every disk, storage and ZFS pool
// (the last 14 days, hourly averages) gives a growth rate by linear regression. When a check
// is evaluated, the rate and its current fill level yield "forecast.days" – the days until it
// is full – which thresholds judge like any metric (default: warn below 30, crit below 7
// days) and which the page "Ablaufdaten" lists.
const (
	forecastEvery   = time.Hour
	forecastWindow  = 14 * 86400
	forecastMinSpan = 2 * 86400 // at least two days of history after the last cleanup
	forecastMinRate = 0.05      // percentage points per day; slower growth counts as stable
	forecastMaxDays = 365       // further away is no forecast
	forecastDrop    = 3.0       // a drop by more percentage points (cleanup, resize) restarts the trend
	forecastMetric  = "forecast.days"
	factFull        = "Voll voraussichtlich"
)

// forecastSources are the fill level metrics (percent) a forecast is made for.
var forecastSources = []string{"disk.usage", "storage.usage", "zfs.usage"}

func forecastKey(deviceID, checkKey, instance string) string {
	return deviceID + "\x00" + checkKey + "\x00" + instance
}

// forecastTick starts a forecast run when one is due (called by the Run loop).
func (c *Core) forecastTick() {
	now := c.Now()
	if now.Before(c.fcNext) || !c.fcRunning.CompareAndSwap(false, true) {
		return
	}
	c.fcNext = now.Add(forecastEvery)
	c.running.Add(1)
	go func() {
		defer c.running.Done()
		defer c.fcRunning.Store(false)
		if err := c.UpdateForecasts(); err != nil {
			log.Printf("forecast: %v", err)
		}
	}()
}

// UpdateForecasts computes the growth rates from the stored history.
func (c *Core) UpdateForecasts() error {
	now := c.Now().Unix()
	series, err := c.St.LatestSeries(forecastSources...)
	if err != nil {
		return err
	}
	var ids []int64
	byID := map[int64]store.Series{}
	for _, s := range series {
		if s.CheckKey == "" || now-s.LastTS > 86400 {
			continue // stale (device removed, disk gone)
		}
		ids = append(ids, s.ID)
		byID[s.ID] = s
	}
	growth := map[string]float64{}
	for chunk := range slices.Chunk(ids, 200) {
		pts, err := c.St.Query(chunk, now-forecastWindow, now, 3600)
		if err != nil {
			return err
		}
		for id, p := range pts {
			if rate, ok := growthRate(p); ok {
				s := byID[id]
				growth[forecastKey(s.DeviceID, s.CheckKey, s.Instance)] = rate
			}
		}
	}
	c.mu.Lock()
	c.growth = growth
	c.mu.Unlock()
	return nil
}

// growthRate returns the growth in percentage points per day of hourly fill levels: a least
// squares line through the points after the last big drop. ok is false without a clear growth
// (too little history, stable or shrinking).
func growthRate(pts []store.Point) (float64, bool) {
	var ok []store.Point
	for _, p := range pts {
		if !math.IsNaN(p.Avg) && !math.IsInf(p.Avg, 0) {
			ok = append(ok, p)
		}
	}
	start := 0
	for i := 1; i < len(ok); i++ {
		if ok[i].Avg < ok[i-1].Avg-forecastDrop {
			start = i
		}
	}
	ok = ok[start:]
	if len(ok) < 24 || ok[len(ok)-1].TS-ok[0].TS < forecastMinSpan {
		return 0, false
	}
	var sx, sy, sxx, sxy float64
	n := float64(len(ok))
	t0 := float64(ok[0].TS)
	for _, p := range ok {
		x := (float64(p.TS) - t0) / 86400
		sx += x
		sy += p.Avg
		sxx += x * x
		sxy += x * p.Avg
	}
	den := n*sxx - sx*sx
	if den <= 0 {
		return 0, false
	}
	rate := (n*sxy - sx*sy) / den
	if rate < forecastMinRate {
		return 0, false
	}
	return rate, true
}

// withForecast adds forecast.days to the fill level metrics of a check that has a growth rate
// (from its current value) and the facts "Voll voraussichtlich" and "Zuwachs". Checks with an
// own estimate (Proxmox Backup Server datastores) are left alone. c.mu must be held.
func (c *Core) withForecast(deviceID string, ch model.Check, now int64) model.Check {
	if len(c.growth) == 0 || ch.Facts[factFull] != "" {
		return ch
	}
	var add []model.Metric
	var facts map[string]string
	sources := 0
	for _, m := range ch.Metrics {
		if slices.Contains(forecastSources, m.Name) {
			sources++
		}
	}
	for _, m := range ch.Metrics {
		if !slices.Contains(forecastSources, m.Name) || math.IsNaN(m.Value) {
			continue
		}
		rate, ok := c.growth[forecastKey(deviceID, ch.Key, m.Instance)]
		if !ok {
			continue
		}
		days := max(0, (100-m.Value)/rate)
		if days > forecastMaxDays {
			continue
		}
		add = append(add, model.Metric{Name: forecastMetric, Instance: m.Instance, Value: math.Round(days*10) / 10, Unit: "d"})
		if facts == nil {
			facts = maps.Clone(ch.Facts)
			if facts == nil {
				facts = map[string]string{}
			}
		}
		prefix := ""
		if sources > 1 {
			prefix = m.Instance + ": "
		}
		facts[factFull] = joinFact(facts[factFull], prefix+time.Unix(now+int64(days*86400), 0).Local().Format("02.01.2006"))
		facts["Zuwachs"] = joinFact(facts["Zuwachs"], prefix+fmtNum(rate)+" % pro Tag")
	}
	if len(add) == 0 {
		return ch
	}
	ch.Metrics = append(slices.Clip(ch.Metrics), add...)
	ch.Facts = facts
	return ch
}

func joinFact(old, add string) string {
	if old == "" {
		return add
	}
	return old + " · " + add
}
