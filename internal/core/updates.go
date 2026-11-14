package core

import (
	"slices"
	"strings"

	"pk95monitoring/internal/model"
)

// UpdateRow is the update state of one system: an agent's "updates" check or a Proxmox
// node's "pve:updates:<node>" check.
type UpdateRow struct {
	DeviceID string `json:"device_id"`
	Device   string `json:"device"` // display name ("<parent> / <node>" for an attached agent)
	Name     string `json:"name"`   // check name
	CheckKey string `json:"check_key"`
	Group    string `json:"group,omitempty"`
	Type     string `json:"type"`
	OS       string `json:"os,omitempty"`
	Platform string `json:"platform,omitempty"`
	State    string `json:"state"`
	Acked    bool   `json:"acked,omitempty"`
	Message  string `json:"message"`
	// Pending, Security, Age (days since the last installation) and Failed are nil when the
	// system does not report them.
	Pending     *float64 `json:"pending"`
	Security    *float64 `json:"security"`
	Age         *float64 `json:"age"`
	Failed      *float64 `json:"failed"`
	Reboot      bool     `json:"reboot"`
	LastInstall string   `json:"last_install,omitempty"`
	Titles      string   `json:"titles,omitempty"`
	Updated     int64    `json:"updated"` // last poll or report
}

// isUpdateCheck reports whether a check key is an update check.
func isUpdateCheck(key string) bool {
	return key == "updates" || strings.HasPrefix(key, "pve:updates:")
}

// Updates lists the update state of every enabled device that reports one, worst first.
func (c *Core) Updates() []UpdateRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []UpdateRow{}
	for id, d := range c.devices {
		rt := c.rt[id]
		if rt == nil || !d.Enabled {
			continue
		}
		for _, cs := range rt.st.Checks {
			if !isUpdateCheck(cs.Key) {
				continue
			}
			r := UpdateRow{DeviceID: id, Device: c.displayName(d), Name: cs.Name, CheckKey: cs.Key, Group: d.Group, Type: d.Type,
				State: cs.State, Acked: cs.Acked, Message: cs.Message, Updated: rt.st.LastPoll,
				Reboot:      strings.HasPrefix(cs.Message, "Neustart erforderlich"),
				LastInstall: cs.Facts["Letzte Installation"], Titles: cs.Facts["Ausstehend"]}
			if p, ok := c.parentOf(d); ok && r.Group == "" {
				r.Group = p.Group
			}
			if d.Agent != nil {
				r.OS, r.Platform = d.Agent.OS, platformText(*d.Agent)
			}
			for _, m := range cs.Metrics {
				v := m.Value
				switch m.Name {
				case "updates.pending":
					r.Pending = &v
				case "updates.security":
					r.Security = &v
				case "updates.age":
					r.Age = &v
				case "updates.failed":
					r.Failed = &v
				}
			}
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b UpdateRow) int {
		ra, rb := model.Rank(a.State), model.Rank(b.State)
		if a.Acked {
			ra = 0
		}
		if b.Acked {
			rb = 0
		}
		if ra != rb {
			return rb - ra
		}
		if r := strings.Compare(strings.ToLower(a.Device), strings.ToLower(b.Device)); r != 0 {
			return r
		}
		return strings.Compare(a.CheckKey, b.CheckKey)
	})
	return out
}

// ExpiryRow is a certificate or domain with its expiry: a check with the metric cert.days
// (HTTPS and TLS checks) or domain.days, or a domain check whose registry publishes no date.
type ExpiryRow struct {
	DeviceID string   `json:"device_id"`
	Device   string   `json:"device"`
	Group    string   `json:"group,omitempty"`
	CheckKey string   `json:"check_key"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind"` // cert, domain
	Days     *float64 `json:"days"` // nil = no expiry date known
	Until    string   `json:"until,omitempty"`
	Detail   string   `json:"detail,omitempty"` // issuer or registrar
	State    string   `json:"state"`
	Acked    bool     `json:"acked,omitempty"`
	Message  string   `json:"message"`
}

// Expiry lists the certificates and domains of all enabled devices, soonest first (unknown
// dates last).
func (c *Core) Expiry() []ExpiryRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []ExpiryRow{}
	for id, d := range c.devices {
		rt := c.rt[id]
		if rt == nil || !d.Enabled {
			continue
		}
		for _, cs := range rt.st.Checks {
			r := ExpiryRow{DeviceID: id, Device: c.displayName(d), Group: d.Group, CheckKey: cs.Key, Name: cs.Name,
				State: cs.State, Acked: cs.Acked, Message: cs.Message}
			for _, m := range cs.Metrics {
				if m.Name == "cert.days" || m.Name == "domain.days" {
					v := m.Value
					r.Days, r.Kind = &v, strings.TrimSuffix(m.Name, ".days")
				}
			}
			if _, ok := cs.Facts["RDAP-Server"]; ok {
				r.Kind = "domain"
			}
			switch r.Kind {
			case "cert":
				r.Until, r.Detail = cs.Facts["Zertifikat gültig bis"], cs.Facts["Aussteller"]
				if r.Until == "" {
					r.Until = cs.Facts["Gültig bis"] // Proxmox node certificates
				}
			case "domain":
				r.Until, r.Detail = cs.Facts["Läuft ab"], cs.Facts["Registrar"]
			default:
				continue
			}
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b ExpiryRow) int {
		switch {
		case a.Days != nil && b.Days != nil && *a.Days != *b.Days:
			if *a.Days < *b.Days {
				return -1
			}
			return 1
		case (a.Days == nil) != (b.Days == nil):
			if a.Days == nil {
				return 1
			}
			return -1
		}
		return strings.Compare(strings.ToLower(a.Device+a.Name), strings.ToLower(b.Device+b.Name))
	})
	return out
}
