package core

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"unicode"

	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

// Attached devices (model.Device.ParentID): an agent – or another device that monitors the same
// thing a second way, e.g. the SNMP view of a UniFi console – can be attached to a device
// (Proxmox cluster, SNMP, net, UniFi …) whose data it complements. The attached device keeps
// its own checks, alerts and detail page; lists show only the parent, whose state includes
// the attached devices (see combine). There is one level: a parent is never attached itself.

const maxNodeLen = 100

// InputError is a request the caller has to correct (HTTP 400); Msg is German.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// childLabel names an attached agent inside its parent: the node, else the agent name.
func childLabel(d model.Device) string {
	if d.Node != "" {
		return d.Node
	}
	return d.Name
}

// parentOf returns the device d is attached to (only when it exists and is not an agent).
// Caller holds c.mu.
func (c *Core) parentOf(d model.Device) (model.Device, bool) {
	if d.ParentID == "" || d.ParentID == d.ID {
		return model.Device{}, false
	}
	p, ok := c.devices[d.ParentID]
	if !ok || p.Type == model.TypeAgent {
		return model.Device{}, false
	}
	return p, true
}

// attached reports whether d is shown inside a parent. Caller holds c.mu.
func (c *Core) attached(d model.Device) bool {
	_, ok := c.parentOf(d)
	return ok
}

// displayName is the device name used in alerts and notifications:
// "<parent> / <node or agent>" for an attached agent. Caller holds c.mu.
func (c *Core) displayName(d model.Device) string {
	if p, ok := c.parentOf(d); ok {
		return p.Name + " / " + childLabel(d)
	}
	return d.Name
}

func sortChildren(list []model.Device) {
	slices.SortFunc(list, func(a, b model.Device) int {
		if r := strings.Compare(strings.ToLower(a.Node), strings.ToLower(b.Node)); r != 0 {
			return r
		}
		if r := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); r != 0 {
			return r
		}
		return strings.Compare(a.ID, b.ID)
	})
}

// childMap returns the attached agents per parent ID, sorted by node and name.
// Caller holds c.mu.
func (c *Core) childMap() map[string][]model.Device {
	out := map[string][]model.Device{}
	for _, d := range c.devices {
		if p, ok := c.parentOf(d); ok {
			out[p.ID] = append(out[p.ID], d)
		}
	}
	for _, list := range out {
		sortChildren(list)
	}
	return out
}

// childrenOf returns the agents attached to parentID. Caller holds c.mu.
func (c *Core) childrenOf(parentID string) []model.Device {
	var out []model.Device
	for _, d := range c.devices {
		if d.ParentID == parentID && d.ID != parentID {
			if _, ok := c.parentOf(d); ok {
				out = append(out, d)
			}
		}
	}
	sortChildren(out)
	return out
}

// Attached returns the agents attached to a device, sorted by node and name.
func (c *Core) Attached(parentID string) []model.Device {
	c.mu.Lock()
	list := c.childrenOf(parentID)
	c.mu.Unlock()
	out := make([]model.Device, 0, len(list))
	for _, d := range list {
		out = append(out, d.Clone())
	}
	return out
}

// pveNode is a node of a Proxmox parent as known from its last poll.
type pveNode struct{ name, ip string }

// pveNodes returns the nodes of a Proxmox device from its pve:node:<node> checks.
// Caller holds c.mu.
func (c *Core) pveNodes(id string) []pveNode {
	rt := c.rt[id]
	if rt == nil {
		return nil
	}
	var out []pveNode
	for _, cs := range rt.st.Checks {
		if name, ok := strings.CutPrefix(cs.Key, "pve:node:"); ok && name != "" {
			out = append(out, pveNode{name: name, ip: cs.Facts["IP"]})
		}
	}
	return out
}

func isIP(s string) bool {
	_, err := netip.ParseAddr(strings.Trim(s, "[]"))
	return err == nil
}

// shortHost is the lower-case host name without domain ("pve1.example.lan" → "pve1");
// IP addresses stay as they are.
func shortHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if isIP(h) {
		return h
	}
	s, _, _ := strings.Cut(h, ".")
	return s
}

// nodeFor returns the Proxmox node of parent whose name equals the short hostname
// (case-insensitive), "" when none does. Caller holds c.mu.
func (c *Core) nodeFor(parent model.Device, hostname string) string {
	if parent.Type != model.TypeProxmox {
		return ""
	}
	short := shortHost(hostname)
	for _, n := range c.pveNodes(parent.ID) {
		if short != "" && strings.EqualFold(n.name, short) {
			return n.name
		}
	}
	return ""
}

// checkNode validates a node name for parent and returns its canonical spelling. For a
// Proxmox parent whose nodes are known the node must be one of them. Caller holds c.mu.
func (c *Core) checkNode(parent model.Device, node string) (string, string) {
	node = strings.TrimSpace(node)
	switch {
	case node == "":
		return "", ""
	case len([]rune(node)) > maxNodeLen:
		return node, fmt.Sprintf("Höchstens %d Zeichen.", maxNodeLen)
	case strings.IndexFunc(node, unicode.IsControl) >= 0:
		return node, "Ungültiger Name."
	}
	if parent.Type == model.TypeProxmox {
		nodes := c.pveNodes(parent.ID)
		for _, n := range nodes {
			if strings.EqualFold(n.name, node) {
				return n.name, ""
			}
		}
		if len(nodes) > 0 {
			return node, fmt.Sprintf("Den Knoten %s gibt es in %s nicht.", node, parent.Name)
		}
	}
	return node, ""
}

func agentHostname(d model.Device) string {
	if d.Agent != nil && d.Agent.Hostname != "" {
		return d.Agent.Hostname
	}
	return d.Name
}

// attachable reports whether a device type can be attached to another device or have devices
// attached: backup servers and Microsoft 365 tenants stand alone.
func attachable(typ string) bool {
	return typ != model.TypeM365 && !model.BackupServer(typ)
}

// Attach attaches the device agentID (an agent or another device) to the device parentID
// (node optional; for an agent on a Proxmox parent it defaults to the node named like the
// agent's host). It returns the parent. Field errors ("agent_id", "node") are German;
// store.ErrNotFound when the parent is unknown.
func (c *Core) Attach(parentID, agentID, node string) (model.Device, map[string]string, error) {
	agentID = strings.TrimSpace(agentID)
	errs := map[string]string{}
	now := c.Now().Unix()

	c.mu.Lock()
	p, ok := c.devices[parentID]
	if !ok {
		c.mu.Unlock()
		return model.Device{}, nil, store.ErrNotFound
	}
	a, found := c.devices[agentID]
	_, parentAttached := c.parentOf(p)
	switch {
	case p.Type == model.TypeAgent:
		errs["agent_id"] = "An einen Agent kann nichts angehängt werden."
	case !attachable(p.Type):
		errs["agent_id"] = "An dieses Gerät kann nichts angehängt werden."
	case parentAttached:
		errs["agent_id"] = "Das Gerät ist selbst angehängt – an ein angehängtes Gerät kann nichts weiter angehängt werden."
	case agentID == "":
		errs["agent_id"] = "Gerät auswählen."
	case !found:
		errs["agent_id"] = "Gerät nicht gefunden."
	case a.ID == p.ID:
		errs["agent_id"] = "Ein Gerät kann nicht an sich selbst angehängt werden."
	case !attachable(a.Type):
		errs["agent_id"] = "Dieses Gerät kann nicht angehängt werden."
	case len(c.childrenOf(a.ID)) > 0:
		errs["agent_id"] = "An diesem Gerät hängen selbst Geräte – erst dort lösen."
	}
	if len(errs) == 0 {
		var msg string
		if node, msg = c.checkNode(p, node); msg != "" {
			errs["node"] = msg
		} else if node == "" && a.Type == model.TypeAgent {
			node = c.nodeFor(p, agentHostname(a))
		}
	}
	if len(errs) == 0 && node != "" {
		for _, o := range c.childrenOf(p.ID) {
			if o.ID != a.ID && strings.EqualFold(o.Node, node) {
				errs["node"] = fmt.Sprintf("Am Knoten %s ist bereits %s angehängt.", node, o.Name)
				break
			}
		}
	}
	if len(errs) > 0 {
		c.mu.Unlock()
		return p.Clone(), errs, nil
	}
	a.ParentID, a.Node, a.Updated = p.ID, node, now
	if err := c.St.Put(KindDevice, a.ID, a); err != nil {
		c.mu.Unlock()
		return model.Device{}, nil, err
	}
	c.devices[a.ID] = a
	c.mu.Unlock()

	where := ""
	if node != "" {
		where = " (Knoten " + node + ")"
	}
	what := "Der Agent"
	title := "Agent angehängt"
	if a.Type != model.TypeAgent {
		what, title = "Das Gerät", "Gerät angehängt"
	}
	c.Event("info", p.Name, title, fmt.Sprintf("%s %s gehört jetzt zu %s%s.", what, a.Name, p.Name, where), nil, false)
	return p.Clone(), nil, nil
}

// Detach makes an attached agent a standalone device again and returns it (unchanged when it
// is not attached). store.ErrNotFound when the device is unknown.
func (c *Core) Detach(agentID string) (model.Device, error) {
	c.mu.Lock()
	a, ok := c.devices[agentID]
	if !ok {
		c.mu.Unlock()
		return model.Device{}, store.ErrNotFound
	}
	if a.ParentID == "" && a.Node == "" {
		c.mu.Unlock()
		return a.Clone(), nil
	}
	parent := a.ParentID
	if p, ok := c.devices[parent]; ok {
		parent = p.Name
	}
	a.ParentID, a.Node, a.Updated = "", "", c.Now().Unix()
	if err := c.St.Put(KindDevice, a.ID, a); err != nil {
		c.mu.Unlock()
		return model.Device{}, err
	}
	c.devices[a.ID] = a
	c.mu.Unlock()
	what, title := "Der Agent", "Agent gelöst"
	if a.Type != model.TypeAgent {
		what, title = "Das Gerät", "Gerät gelöst"
	}
	c.Event("info", a.Name, title, fmt.Sprintf("%s %s gehört nicht mehr zu %s.", what, a.Name, parent), nil, false)
	return a.Clone(), nil
}

// detachChildren makes the agents attached to parentID standalone (the parent is deleted).
// Caller holds c.mu.
func (c *Core) detachChildren(parentID string, now int64) []string {
	var names []string
	for id, d := range c.devices {
		if d.ParentID != parentID || id == parentID {
			continue
		}
		d.ParentID, d.Node, d.Updated = "", "", now
		if err := c.St.Put(KindDevice, id, d); err != nil {
			continue
		}
		c.devices[id] = d
		names = append(names, d.Name)
	}
	slices.Sort(names)
	return names
}

// Suggest returns the devices an unattached agent probably belongs to: Proxmox clusters with
// a node named like the agent's host (or with the agent's IP), and devices whose host is the
// agent's host name or one of its IPs. An unattached SNMP or net device without attached
// devices of its own gets the other devices with the same host (e.g. the UniFi console it
// is the SNMP view of). Empty otherwise.
func (c *Core) Suggest(agentID string) []Suggestion {
	out := []Suggestion{}
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.devices[agentID]
	if !ok || c.attached(a) {
		return out
	}
	if a.Type != model.TypeAgent {
		return c.suggestSameHost(a)
	}
	full := strings.ToLower(strings.TrimSpace(agentHostname(a)))
	short := shortHost(full)
	ips := map[string]bool{}
	if a.Agent != nil {
		for _, ip := range a.Agent.IPs {
			if ad, err := netip.ParseAddr(strings.TrimSpace(ip)); err == nil {
				ips[ad.Unmap().String()] = true
			}
		}
	}
	if ad, err := netip.ParseAddr(a.Host); err == nil {
		ips[ad.Unmap().String()] = true
	}
	isAgentIP := func(s string) bool {
		ad, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(s), "[]"))
		return err == nil && ips[ad.Unmap().String()]
	}
	for _, p := range c.devices {
		if p.Type == model.TypeAgent || p.ID == a.ID {
			continue
		}
		match, node := false, ""
		if p.Type == model.TypeProxmox {
			for _, n := range c.pveNodes(p.ID) {
				if (short != "" && strings.EqualFold(n.name, short)) || (n.ip != "" && isAgentIP(n.ip)) {
					match, node = true, n.name
					break
				}
			}
		}
		if !match {
			h := strings.ToLower(strings.TrimSpace(p.Host))
			switch {
			case h == "":
			case isIP(h):
				match = isAgentIP(h)
			default:
				match = h == full || (short != "" && shortHost(h) == short)
			}
			if match {
				node = c.nodeFor(p, full)
			}
		}
		if match {
			out = append(out, Suggestion{ID: p.ID, Name: p.Name, Type: p.Type, Node: node})
		}
	}
	slices.SortFunc(out, func(x, y Suggestion) int {
		if r := strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)); r != 0 {
			return r
		}
		return strings.Compare(x.ID, y.ID)
	})
	return out
}

// ---- combined state of a parent and its attached agents ----

// suggestSameHost lists the devices an SNMP or net device a probably complements: devices of
// another type with the same host that are not attached themselves. Caller holds c.mu.
func (c *Core) suggestSameHost(a model.Device) []Suggestion {
	out := []Suggestion{}
	host := strings.ToLower(strings.TrimSpace(a.Host))
	if (a.Type != model.TypeSNMP && a.Type != model.TypeNet) || host == "" || len(c.childrenOf(a.ID)) > 0 {
		return out
	}
	for _, p := range c.devices {
		if p.ID == a.ID || p.Type == model.TypeAgent || p.Type == model.TypeSNMP || p.Type == model.TypeNet || !attachable(p.Type) || c.attached(p) {
			continue
		}
		if strings.ToLower(strings.TrimSpace(p.Host)) == host {
			out = append(out, Suggestion{ID: p.ID, Name: p.Name, Type: p.Type})
		}
	}
	slices.SortFunc(out, func(x, y Suggestion) int { return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)) })
	return out
}

// counted reports whether an attached agent counts for its parent's state: disabled agents
// and agents in maintenance do not.
func counted(d model.Device, now int64) bool { return d.Enabled && !d.InMaintenance(now) }

// combined is the state of a parent including its attached agents.
type combined struct {
	state, message  string
	since, lastPoll int64
	checks          CheckCounts
}

// combine merges the status of a device with the statuses of its attached agents: the state
// is the worst known state, the message lists the problems (an agent's prefixed with its node
// or name), the check counts add up. Caller holds c.mu.
func (c *Core) combine(d model.Device, kids []model.Device, now int64) combined {
	own := model.DeviceStatus{State: model.StateUnknown}
	if rt := c.rt[d.ID]; rt != nil {
		own = rt.st
	}
	out := combined{state: own.State, message: own.Message, since: own.Since, lastPoll: own.LastPoll, checks: countChecks(own.Checks)}
	if out.state == "" {
		out.state = model.StateUnknown
	}
	type part struct {
		label string
		st    model.DeviceStatus
	}
	parts := []part{{"", own}}
	for _, k := range kids {
		rt := c.rt[k.ID]
		if rt == nil {
			continue
		}
		n := countChecks(rt.st.Checks)
		out.checks.OK += n.OK
		out.checks.Warn += n.Warn
		out.checks.Crit += n.Crit
		out.checks.Unknown += n.Unknown
		out.lastPoll = max(out.lastPoll, rt.st.LastPoll)
		if counted(k, now) {
			parts = append(parts, part{childLabel(k), rt.st})
		}
	}
	if len(parts) == 1 {
		return out
	}
	worst := ""
	for _, p := range parts {
		if p.st.State == "" || p.st.State == model.StateUnknown {
			continue
		}
		if worst == "" {
			worst = p.st.State
		} else {
			worst = model.Worst(worst, p.st.State)
		}
	}
	if worst == "" {
		return out // nothing known: the parent's own (unknown) state and message
	}
	out.state, out.since = worst, 0
	for _, p := range parts {
		if p.st.State == worst && p.st.Since > 0 && (out.since == 0 || p.st.Since < out.since) {
			out.since = p.st.Since
		}
	}
	if worst == model.StateOK {
		out.message = ""
		return out
	}
	// The message lists the problems of all parts like deviceState does, an agent's prefixed
	// with its node or name.
	type problem struct {
		label string
		cs    model.CheckStatus
	}
	var bad []problem
	for _, p := range parts {
		if p.st.State != model.StateWarn && p.st.State != model.StateCrit {
			continue
		}
		for _, cs := range p.st.Checks {
			if (cs.State == model.StateWarn || cs.State == model.StateCrit) && !model.SeparateCheck(cs.Key) {
				bad = append(bad, problem{p.label, cs})
			}
		}
	}
	slices.SortStableFunc(bad, func(a, b problem) int { return model.Rank(b.cs.State) - model.Rank(a.cs.State) })
	text := func(p problem) string {
		if p.label == "" {
			return problemText(p.cs)
		}
		return p.label + ": " + problemText(p.cs)
	}
	switch len(bad) {
	case 0:
		out.message = stateWord(worst)
		return out
	case 1:
		out.message = text(bad[0])
		return out
	}
	texts := make([]string, 0, 4)
	for i, p := range bad {
		if i == 3 {
			texts = append(texts, "…")
			break
		}
		texts = append(texts, text(p))
	}
	out.message = fmt.Sprintf("%d Probleme: %s", len(bad), strings.Join(texts, "; "))
	return out
}

// agentFacts adds the fact "Agent" to the pve:node:<node> checks of a Proxmox device for
// every attached agent with that node. Caller holds c.mu.
func (c *Core) agentFacts(d model.Device, st *model.DeviceStatus) {
	if d.Type != model.TypeProxmox {
		return
	}
	kids := c.childrenOf(d.ID)
	if len(kids) == 0 {
		return
	}
	for i := range st.Checks {
		cs := &st.Checks[i]
		node, ok := strings.CutPrefix(cs.Key, "pve:node:")
		if !ok {
			continue
		}
		for _, k := range kids {
			if k.Node == "" || !strings.EqualFold(k.Node, node) {
				continue
			}
			state := model.StateUnknown
			if rt := c.rt[k.ID]; rt != nil && rt.st.State != "" {
				state = rt.st.State
			}
			v := k.Name + " · " + stateWord(state)
			if k.Agent != nil && k.Agent.Version != "" {
				v += " · v" + strings.TrimPrefix(k.Agent.Version, "v")
			}
			facts := make(map[string]string, len(cs.Facts)+1)
			for fk, fv := range cs.Facts {
				facts[fk] = fv
			}
			facts["Agent"] = v
			cs.Facts = facts
			break
		}
	}
}
