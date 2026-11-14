package core

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/collect/netcheck"
	"pk95monitoring/internal/model"
)

// netChecksOf returns the network checks of a device; a net device without checks but with
// a host is pinged.
func netChecksOf(d model.Device) []model.NetCheck {
	if len(d.Checks) == 0 && d.Type == model.TypeNet && strings.TrimSpace(d.Host) != "" {
		return []model.NetCheck{{ID: "ping", Type: model.NetPing}}
	}
	return d.Checks
}

// pollable reports whether the scheduler polls the device.
func pollable(d model.Device) bool {
	if !d.Enabled {
		return false
	}
	switch d.Type {
	case model.TypeProxmox, model.TypeSNMP, model.TypeNUT, model.TypeVeeam, model.TypePBS:
		return true
	}
	return len(netChecksOf(d)) > 0
}

// wakeUp makes the scheduler look at the due times now.
func (c *Core) wakeUp() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// schedule runs the polls: every device on its own interval (first run with a random
// delay), at most maxPolls at a time.
func (c *Core) schedule(ctx context.Context) {
	sem := make(chan struct{}, maxPolls)
	var wg sync.WaitGroup
	defer wg.Wait()

	c.mu.Lock()
	start := c.Now()
	for id, d := range c.devices {
		jitter := min(time.Duration(d.PollInterval())*time.Second, c.tm.jitter)
		if jitter > 0 {
			c.rt[id].next = start.Add(rand.N(jitter))
		}
	}
	c.mu.Unlock()

	for {
		c.mu.Lock()
		now := c.Now()
		sleep := c.tm.maxSleep
		var due []string
		for id, d := range c.devices {
			rt := c.rt[id]
			if !pollable(d) || rt.running {
				continue
			}
			if !rt.next.After(now) {
				rt.running = true
				rt.next = now.Add(time.Duration(d.PollInterval()) * time.Second)
				due = append(due, id)
			}
			sleep = min(sleep, rt.next.Sub(now))
		}
		c.mu.Unlock()

		for _, id := range due {
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-sem }()
				c.pollDevice(ctx, id)
				c.mu.Lock()
				if rt := c.rt[id]; rt != nil {
					rt.running = false
				}
				c.mu.Unlock()
				c.wakeUp()
			}()
		}

		t := time.NewTimer(max(sleep, 10*time.Millisecond))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-c.wake:
		case <-t.C:
		}
		t.Stop()
	}
}

// PollNow polls a device immediately (agents: no-op, returns the current status).
func (c *Core) PollNow(ctx context.Context, id string) (model.DeviceStatus, error) {
	d, err := c.Device(id)
	if err != nil {
		return model.DeviceStatus{}, err
	}
	if d.Type != model.TypeAgent || len(netChecksOf(d)) > 0 {
		c.pollDevice(ctx, id)
		c.mu.Lock()
		if rt := c.rt[id]; rt != nil {
			rt.next = c.Now().Add(time.Duration(d.PollInterval()) * time.Second)
		}
		c.mu.Unlock()
	}
	return c.Status(id), nil
}

// pollDevice polls one device (serialized per device) and applies the result.
func (c *Core) pollDevice(ctx context.Context, id string) {
	c.mu.Lock()
	rt, d := c.rt[id], c.devices[id]
	c.mu.Unlock()
	if rt == nil {
		return
	}
	rt.pollMu.Lock()
	defer rt.pollMu.Unlock()
	c.mu.Lock()
	d, ok := c.devices[id] // may have changed while waiting
	c.mu.Unlock()
	if !ok {
		return
	}

	timeout := min(time.Duration(d.PollInterval())*time.Second, maxPollTimeout)
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var (
		wg        sync.WaitGroup
		collected []model.Check
		cerr      error
	)
	switch d.Type {
	case model.TypeProxmox:
		wg.Go(func() { collected, cerr = c.Proxmox.Collect(pctx, d) })
	case model.TypeSNMP:
		wg.Go(func() { collected, cerr = c.SNMP.Collect(pctx, d) })
	case model.TypeNUT:
		wg.Go(func() { collected, cerr = c.NUT.Collect(pctx, d) })
	case model.TypeVeeam:
		wg.Go(func() { collected, cerr = c.Veeam.Collect(pctx, d) })
	case model.TypePBS:
		wg.Go(func() { collected, cerr = c.PBS.Collect(pctx, d) })
	}
	ncs := netChecksOf(d)
	nets := make([]model.Check, len(ncs))
	for i, nc := range ncs {
		wg.Go(func() { nets[i] = netcheck.Run(pctx, nc, d.Host) })
	}
	wg.Wait()
	if ctx.Err() != nil {
		return // shutdown or caller gone: the result says nothing about the device
	}
	if cerr != nil && errors.Is(pctx.Err(), context.DeadlineExceeded) {
		cerr = fmt.Errorf("Keine Antwort innerhalb von %d Sekunden", int(timeout.Seconds()))
	}
	now := c.Now().Unix()
	kept, ok := c.applyPoll(id, collected, cerr, nets, now)
	if !ok {
		return
	}
	c.writeMetrics(id, now, kept)
}

// writeMetrics stores the samples of a device unless it was deleted meanwhile.
func (c *Core) writeMetrics(id string, ts int64, checks []model.Check) {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	c.mu.Lock()
	_, ok := c.devices[id]
	c.mu.Unlock()
	if !ok {
		return // DeleteDevice already removed (or is about to remove) its data
	}
	if err := c.St.WriteMetrics(id, ts, checks); err != nil {
		log.Printf("write metrics %s: %v", id, err)
	}
}

// applyPoll merges a poll result into the status and returns the checks whose metrics are
// stored (checks switched off by a rule are dropped and listed in Hidden). It returns false
// when the device is gone.
func (c *Core) applyPoll(id string, collected []model.Check, cerr error, nets []model.Check, now int64) ([]model.Check, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.devices[id]
	rt := c.rt[id]
	if !ok || rt == nil {
		return nil, false
	}
	if cerr == nil && d.Type != model.TypeNet && d.Type != model.TypeAgent {
		discover(d, rt, collected, now)
		if dc, ok := discoveryCheck(rt); ok {
			collected = append(slices.Clip(collected), dc)
		}
	}
	collected, hidden := splitOff(d, collected)
	nets, hiddenNets := splitOff(d, nets)
	prev := map[string]model.CheckStatus{}
	for _, cs := range rt.st.Checks {
		prev[cs.Key] = cs
	}
	global := c.Settings().Thresholds
	eval := func(ch model.Check) model.CheckStatus { return c.evaluate(d, rt, prev, ch, now, global) }

	var out []model.CheckStatus
	switch d.Type {
	case model.TypeProxmox, model.TypeSNMP, model.TypeNUT, model.TypeVeeam, model.TypePBS:
		rt.st.LastPoll = now
		if cerr == nil {
			rt.fails, rt.st.Error, rt.st.LastOK = 0, "", now
			out = append(out, eval(reachCheck(model.StateOK, "Erreichbar")))
			rt.st.Hidden = replaceHidden(rt.st.Hidden, hidden, scopeOther)
			for _, ch := range collected {
				if ch.Key != "" && ch.Key != reachKey && !strings.HasPrefix(ch.Key, "net:") {
					out = append(out, eval(ch))
				}
			}
		} else {
			rt.fails++
			state := model.StateWarn
			if rt.fails >= 2 {
				state = model.StateCrit
			}
			rt.st.Error = cerr.Error()
			out = append(out, eval(reachCheck(state, cerr.Error())))
			for _, cs := range rt.st.Checks {
				if cs.Key != reachKey && !strings.HasPrefix(cs.Key, "net:") {
					out = append(out, staleStatus(cs, now))
				}
			}
		}
	case model.TypeNet:
		rt.st.LastPoll, rt.st.LastOK, rt.st.Error = now, now, ""
	case model.TypeAgent:
		for _, cs := range rt.st.Checks {
			if !strings.HasPrefix(cs.Key, "net:") {
				out = append(out, cs)
			}
		}
	}
	for _, ch := range nets {
		out = append(out, eval(ch))
	}
	rt.st.Hidden = replaceHidden(rt.st.Hidden, hiddenNets, scopeNet)
	c.finish(d, rt, out, now)
	return append(slices.Clip(collected), nets...), true
}
