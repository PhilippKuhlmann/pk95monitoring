package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"slices"
	"strings"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

// agentSaveEvery is how often LastSeen alone is written to the device (seconds).
const agentSaveEvery = 300

// updateRetry is how long the same update is not offered again to an agent that still
// reports the version it had when the update was offered (seconds).
const updateRetry = 6 * 3600

// firstIP returns the first usable address (no loopback, link-local or unspecified).
func firstIP(ips []string, fallback string) string {
	for _, s := range append(slices.Clone(ips), fallback) {
		a, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() || a.IsMulticast() {
			continue
		}
		return a.Unmap().String()
	}
	return ""
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// RegisterAgent registers (or re-registers, matched by AgentID) an agent and returns its new
// token. ErrBadEnrollKey when the key is wrong. With req.DeviceID the agent is attached to that
// device (Node defaults, for a Proxmox device, to the node named like the host); an unknown
// device or an agent there is an *InputError. Without DeviceID a re-registered agent stays
// attached where it was.
func (c *Core) RegisterAgent(ctx context.Context, req model.AgentRegister, remoteIP string) (model.AgentRegistered, error) {
	if !c.checkEnrollKey(req.Key) {
		return model.AgentRegistered{}, ErrBadEnrollKey
	}
	req.AgentID, req.Hostname = clip(req.AgentID, 128), clip(req.Hostname, 100)
	req.DeviceID, req.Node = clip(req.DeviceID, 64), clip(req.Node, maxNodeLen)
	if req.Hostname == "" {
		req.Hostname = "Agent " + remoteIP
	}
	if req.AgentID == "" {
		req.AgentID = req.Hostname
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return model.AgentRegistered{}, err
	}
	token := hex.EncodeToString(b)
	now := c.Now().Unix()

	c.mu.Lock()
	var d model.Device
	found := false
	for _, x := range c.devices {
		if x.Type == model.TypeAgent && x.Agent != nil && x.Agent.AgentID == req.AgentID {
			d, found = x, true
			break
		}
	}
	if req.DeviceID != "" {
		p, ok := c.devices[req.DeviceID]
		var msg string
		switch {
		case !ok:
			msg = fmt.Sprintf("Das Gerät %q gibt es auf dem Server nicht (Option --device).", req.DeviceID)
		case p.Type == model.TypeAgent:
			msg = fmt.Sprintf("Das Gerät %s ist selbst ein Agent; Agents lassen sich nur an andere Geräte anhängen.", p.Name)
		default:
			req.Node, msg = c.checkNode(p, req.Node)
			if msg != "" {
				msg = "Option --node: " + msg
			} else if req.Node == "" {
				req.Node = c.nodeFor(p, req.Hostname)
			}
		}
		if msg != "" {
			c.mu.Unlock()
			return model.AgentRegistered{}, &InputError{Msg: msg}
		}
	}
	if !found {
		d = model.Device{ID: newID(8), Name: req.Hostname, Type: model.TypeAgent, Enabled: true,
			Host: firstIP(req.IPs, remoteIP), Checks: []model.NetCheck{}, Thresholds: []model.Threshold{},
			Created: now, Agent: &model.AgentConfig{AgentID: req.AgentID, Services: []string{}, IgnoreServices: []string{},
				EventOff: []string{}, EventRules: []model.EventRule{}, EventIgnore: []model.EventIgnore{}}}
	}
	a := *d.Agent
	a.Hostname, a.OS, a.Platform = req.Hostname, clip(req.OS, 32), clip(req.Platform, 100)
	a.PlatformVersion, a.Kernel, a.Arch = clip(req.PlatformVersion, 100), clip(req.Kernel, 100), clip(req.Arch, 32)
	a.Version, a.IPs = clip(req.Version, 64), clipIPs(req.IPs)
	d.Agent, d.Updated = &a, now
	if d.Host == "" {
		d.Host = firstIP(req.IPs, remoteIP)
	}
	if req.DeviceID != "" {
		d.ParentID, d.Node = req.DeviceID, req.Node
	}
	parent := ""
	if p, ok := c.parentOf(d); ok {
		parent = p.Name
		if d.Node != "" {
			parent += " (Knoten " + d.Node + ")"
		}
	}
	if err := c.St.Put(KindDevice, d.ID, d); err != nil {
		c.mu.Unlock()
		return model.AgentRegistered{}, err
	}
	c.devices[d.ID] = d
	if c.rt[d.ID] == nil {
		c.rt[d.ID] = newDevState(d.ID)
	}
	c.rt[d.ID].saved = a.LastSeen
	c.mu.Unlock()

	if err := c.St.SetAgentToken(d.ID, token); err != nil {
		return model.AgentRegistered{}, err
	}
	what := "neu"
	if found {
		what = "erneut"
	}
	body := fmt.Sprintf("%s (%s) hat sich %s registriert, Adresse %s.", req.Hostname, platformText(a), what, remoteIP)
	if parent != "" {
		body += " Gehört zu " + parent + "."
	}
	c.Event("info", d.Name, "Agent registriert", body, nil, false)
	c.wakeUp()
	return model.AgentRegistered{DeviceID: d.ID, Token: token, Interval: d.PollInterval()}, nil
}

func clipIPs(ips []string) []string {
	out := []string{}
	for _, ip := range ips {
		if ip = clip(ip, 64); ip != "" && len(out) < 16 {
			out = append(out, ip)
		}
	}
	return out
}

// platformText is the readable operating system, e.g. "Debian 12" or
// "Microsoft Windows Server 2022 Standard".
func platformText(a model.AgentConfig) string {
	p := a.Platform
	if p == "" {
		return a.OS
	}
	if a.OS == "windows" || strings.Contains(p, " ") {
		return p
	}
	if p[0] >= 'a' && p[0] <= 'z' {
		p = strings.ToUpper(p[:1]) + p[1:]
	}
	if v := a.PlatformVersion; v != "" {
		p += " " + v
	}
	return p
}

// AgentReport authenticates the token and ingests the report. ErrUnauthorized when the token
// is unknown.
func (c *Core) AgentReport(ctx context.Context, token string, rep model.AgentReport, remoteIP string) (model.AgentReply, error) {
	if strings.TrimSpace(token) == "" {
		return model.AgentReply{}, ErrUnauthorized
	}
	id, err := c.St.AgentDevice(token)
	if errors.Is(err, store.ErrNotFound) {
		return model.AgentReply{}, ErrUnauthorized
	}
	if err != nil {
		return model.AgentReply{}, err
	}
	reply, err := c.IngestReport(id, rep)
	if errors.Is(err, store.ErrNotFound) {
		return reply, ErrUnauthorized
	}
	return reply, err
}

// IngestReport ingests a report for a known agent device (used by AgentReport and the demo).
// The reply carries the agent configuration and, with auto-update on and an older agent
// version, the update. Buffered reports and reports older than the last evaluated one only
// add history.
func (c *Core) IngestReport(deviceID string, rep model.AgentReport) (model.AgentReply, error) {
	now := c.Now().Unix()
	ts := rep.Time
	switch {
	case ts <= 0 || ts > now:
		ts = now
	case !rep.Buffered && now-ts > agentSaveEvery:
		ts = now // clock of the agent is off
	}
	checks := make([]model.Check, 0, len(rep.Checks))
	for _, ch := range rep.Checks {
		if ch.Key == "" || ch.Key == reachKey || strings.HasPrefix(ch.Key, "net:") || len(checks) >= 2000 {
			continue
		}
		checks = append(checks, ch)
	}

	c.mu.Lock()
	d, ok := c.devices[deviceID]
	rt := c.rt[deviceID]
	if !ok || rt == nil {
		c.mu.Unlock()
		return model.AgentReply{}, store.ErrNotFound
	}
	if d.Type != model.TypeAgent {
		c.mu.Unlock()
		return model.AgentReply{}, fmt.Errorf("Gerät %s ist kein Agent", d.Name)
	}
	if !rep.Buffered && ts >= rt.lastEval {
		// a new agent version may report new or renamed checks: those are expected
		if d.Agent != nil && d.Agent.Version != "" && rep.Version != "" && rep.Version != d.Agent.Version {
			rt.acceptUnseen = true
		}
		discover(d, rt, checks, now)
		if dc, ok := discoveryCheck(rt); ok {
			checks = append(checks, dc)
		}
	}
	// Checks switched off by a rule are dropped with their metrics, buffered ones too.
	checks, hidden := splitOff(d, checks)
	a := model.AgentConfig{}
	if d.Agent != nil {
		a = *d.Agent
	}
	before := a
	if rep.Hostname != "" {
		a.Hostname = clip(rep.Hostname, 100)
	}
	if rep.OS != "" {
		a.OS, a.Platform, a.PlatformVersion = clip(rep.OS, 32), clip(rep.Platform, 100), clip(rep.PlatformVersion, 100)
		a.Kernel, a.Arch = clip(rep.Kernel, 100), clip(rep.Arch, 32)
	}
	if rep.Version != "" {
		a.Version = clip(rep.Version, 64)
	}
	if rep.IPs != nil {
		a.IPs = clipIPs(rep.IPs)
	}
	a.LastSeen = now
	before.LastSeen = a.LastSeen
	changed := !agentEqual(before, a)
	d.Agent = &a
	if d.Host == "" {
		d.Host = firstIP(a.IPs, "")
		changed = changed || d.Host != ""
	}
	c.devices[d.ID] = d
	if changed || now-rt.saved >= agentSaveEvery {
		if err := c.St.Put(KindDevice, d.ID, d); err != nil {
			log.Printf("save device %s: %v", d.ID, err)
		} else {
			rt.saved = now
		}
	}
	rt.st.LastPoll, rt.st.LastOK, rt.st.Error = now, now, ""
	rt.dirty = true
	if !rep.Buffered && ts >= rt.lastEval {
		rt.lastEval = ts
		prev := map[string]model.CheckStatus{}
		for _, cs := range rt.st.Checks {
			prev[cs.Key] = cs
		}
		global := c.Settings().Thresholds
		out := []model.CheckStatus{c.evaluate(d, rt, prev, reachCheck(model.StateOK, "Agent meldet sich"), now, global)}
		for _, ch := range checks {
			out = append(out, c.evaluate(d, rt, prev, ch, now, global))
		}
		for _, cs := range rt.st.Checks {
			if strings.HasPrefix(cs.Key, "net:") {
				out = append(out, cs)
			}
		}
		rt.st.Hidden = replaceHidden(rt.st.Hidden, hidden, scopeOther)
		c.finish(d, rt, out, now)
	}
	c.mu.Unlock()

	c.writeMetrics(d.ID, ts, checks)
	reply := c.agentReply(d, rep)
	if u := reply.Update; u != nil {
		c.mu.Lock()
		if o := rt.offer; o.sha == u.SHA256 && o.from == rep.Version && now-o.at < updateRetry {
			reply.Update = nil // offered already and the agent still runs the old version
		} else {
			rt.offer = updateOffer{sha: u.SHA256, from: rep.Version, at: now}
		}
		c.mu.Unlock()
	}
	return reply, nil
}

func agentEqual(a, b model.AgentConfig) bool {
	return a.Hostname == b.Hostname && a.OS == b.OS && a.Platform == b.Platform &&
		a.PlatformVersion == b.PlatformVersion && a.Kernel == b.Kernel && a.Arch == b.Arch &&
		a.Version == b.Version && slices.Equal(a.IPs, b.IPs)
}

// agentReply is the configuration the agent should use, with an update when one is due.
func (c *Core) agentReply(d model.Device, rep model.AgentReport) model.AgentReply {
	r := model.AgentReply{Interval: d.PollInterval(), Services: []string{}, IgnoreServices: []string{}}
	if d.Agent != nil {
		r.Services = append(r.Services, d.Agent.Services...)
		r.WatchAutoServices = d.Agent.WatchAutoServices
		r.IgnoreServices = append(r.IgnoreServices, d.Agent.IgnoreServices...)
		r.CentralServices = slices.Clone(c.Settings().Agent.centralServices(d.Agent.OS))
	}
	if r.CentralServices == nil {
		r.CentralServices = []string{}
	}
	r.EventRules, r.EventIgnore = c.AgentEvents(d)
	ver := c.Agents.Version()
	if !c.Settings().Agent.AutoUpdate || ver == "" || rep.Version == "" || rep.Version == ver {
		return r
	}
	if bin, _, ok := c.Agents.Lookup(rep.OS, rep.Arch); ok {
		r.Update = &model.AgentUpdate{Version: ver, URL: "/agent/download/" + rep.OS + "/" + rep.Arch, SHA256: bin.SHA256}
	}
	return r
}
