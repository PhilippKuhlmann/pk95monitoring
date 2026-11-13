package core

import (
	"context"
	"errors"
	"slices"
	"strings"

	"pk95monitoring/internal/model"
)

// Availability reports are derived from the alerts (kept 400 days): a device is down while
// its reach check has an alert (unreachable, agent silent); a check has a problem while it
// has an alert. Maintenance raises no alerts, so it counts as available. An alert's time
// counts with the severity it has (or had when it ended).

// MaxReportDays is the longest period a report covers (the alert retention).
const MaxReportDays = 400

// ReportCheck is one check with alerts in the period.
type ReportCheck struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Alerts  int    `json:"alerts"`
	Crit    int64  `json:"crit"`    // seconds with a critical alert
	Warn    int64  `json:"warn"`    // seconds with a warning
	Longest int64  `json:"longest"` // longest alert, seconds
}

// ReportDevice is the availability of one device in the period.
type ReportDevice struct {
	ID       string `json:"id"`
	Name     string `json:"name"` // "<parent> / <node>" for an attached agent
	Type     string `json:"type"`
	Group    string `json:"group,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	Enabled  bool   `json:"enabled"`
	// Covered is the part of the period the device existed (seconds).
	Covered int64 `json:"covered"`
	// Availability is the percentage of Covered without an outage.
	Availability float64       `json:"availability"`
	Downtime     int64         `json:"downtime"` // seconds unreachable
	Outages      int           `json:"outages"`
	Longest      int64         `json:"longest"`      // longest outage, seconds
	Alerts       int           `json:"alerts"`       // alerts of all checks
	ProblemTime  int64         `json:"problem_time"` // seconds with at least one alert (reach excluded)
	Checks       []ReportCheck `json:"checks"`       // checks with alerts, worst first
}

// Report is the availability of all devices in [From, To).
type Report struct {
	From     int64          `json:"from"`
	To       int64          `json:"to"`
	Instance string         `json:"instance"`
	Devices  []ReportDevice `json:"devices"` // least available first
	// Availability is the mean over the devices, weighted by their covered time; nil
	// without devices.
	Availability *float64 `json:"availability"`
	Downtime     int64    `json:"downtime"`
	Outages      int      `json:"outages"`
	Alerts       int      `json:"alerts"`
}

// ErrReportPeriod is returned for an empty or too long period.
var ErrReportPeriod = errors.New("Zeitraum ungültig: Ende nach Beginn und höchstens 400 Tage")

type span struct{ from, to int64 }

// unionLen is the length of the union of spans.
func unionLen(list []span) int64 {
	if len(list) == 0 {
		return 0
	}
	slices.SortFunc(list, func(a, b span) int { return int(a.from - b.from) })
	var total int64
	cur := list[0]
	for _, s := range list[1:] {
		if s.from <= cur.to {
			cur.to = max(cur.to, s.to)
			continue
		}
		total += cur.to - cur.from
		cur = s
	}
	return total + cur.to - cur.from
}

// Report computes the availability of every device in [from, to); to is capped at now.
func (c *Core) Report(ctx context.Context, from, to int64) (Report, error) {
	now := c.Now().Unix()
	to = min(to, now)
	if to <= from || to-from > MaxReportDays*86400 {
		return Report{}, ErrReportPeriod
	}
	alerts, err := c.St.AlertsBetween(ctx, from, to)
	if err != nil {
		return Report{}, err
	}
	byDev := map[string][]model.Alert{}
	for _, a := range alerts {
		byDev[a.DeviceID] = append(byDev[a.DeviceID], a)
	}

	c.mu.Lock()
	devs := make([]model.Device, 0, len(c.devices))
	names := map[string]string{}
	for _, d := range c.devices {
		devs = append(devs, d)
		names[d.ID] = c.displayName(d)
	}
	c.mu.Unlock()

	rep := Report{From: from, To: to, Instance: c.InstanceName(), Devices: []ReportDevice{}}
	var covSum, upSum int64
	for _, d := range devs {
		start := max(from, d.Created)
		if start >= to {
			continue // created after the period
		}
		rd := ReportDevice{ID: d.ID, Name: names[d.ID], Type: d.Type, Group: d.Group, ParentID: d.ParentID, Enabled: d.Enabled,
			Covered: to - start, Checks: []ReportCheck{}}
		checks := map[string]*ReportCheck{}
		var down, problems []span
		for _, a := range byDev[d.ID] {
			end := a.Ended
			if end == 0 {
				end = now
			}
			s := span{max(a.Started, start), min(end, to)}
			if s.to <= s.from {
				continue
			}
			dur := s.to - s.from
			rd.Alerts++
			if a.CheckKey == reachKey {
				down = append(down, s)
				rd.Outages++
				rd.Longest = max(rd.Longest, dur)
				continue
			}
			problems = append(problems, s)
			rc := checks[a.CheckKey]
			if rc == nil {
				rc = &ReportCheck{Key: a.CheckKey, Name: a.CheckName}
				checks[a.CheckKey] = rc
			}
			rc.Alerts++
			rc.Longest = max(rc.Longest, dur)
			if a.Severity == model.StateCrit {
				rc.Crit += dur
			} else {
				rc.Warn += dur
			}
		}
		rd.Downtime = unionLen(down)
		rd.ProblemTime = unionLen(problems)
		rd.Availability = 100 * float64(rd.Covered-rd.Downtime) / float64(rd.Covered)
		for _, rc := range checks {
			rd.Checks = append(rd.Checks, *rc)
		}
		slices.SortFunc(rd.Checks, func(a, b ReportCheck) int {
			if a.Crit != b.Crit {
				return int(b.Crit - a.Crit)
			}
			if a.Warn != b.Warn {
				return int(b.Warn - a.Warn)
			}
			return strings.Compare(a.Name, b.Name)
		})
		rep.Devices = append(rep.Devices, rd)
		covSum += rd.Covered
		upSum += rd.Covered - rd.Downtime
		rep.Downtime += rd.Downtime
		rep.Outages += rd.Outages
		rep.Alerts += rd.Alerts
	}
	if covSum > 0 {
		av := 100 * float64(upSum) / float64(covSum)
		rep.Availability = &av
	}
	slices.SortFunc(rep.Devices, func(a, b ReportDevice) int {
		switch {
		case a.Availability < b.Availability:
			return -1
		case a.Availability > b.Availability:
			return 1
		case a.ProblemTime != b.ProblemTime:
			return int(b.ProblemTime - a.ProblemTime)
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return rep, nil
}
