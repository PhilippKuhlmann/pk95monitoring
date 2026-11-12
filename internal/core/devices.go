package core

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/collect/netcheck"
	"pk95monitoring/internal/collect/nut"
	"pk95monitoring/internal/collect/proxmox"
	"pk95monitoring/internal/collect/snmp"
	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

// sortDevices orders by group (devices without group last) and name.
func sortDevices(list []model.Device) {
	slices.SortFunc(list, func(a, b model.Device) int {
		ga, gb := strings.ToLower(a.Group), strings.ToLower(b.Group)
		if (ga == "") != (gb == "") {
			if ga == "" {
				return 1
			}
			return -1
		}
		if r := strings.Compare(ga, gb); r != 0 {
			return r
		}
		if r := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); r != 0 {
			return r
		}
		return strings.Compare(a.ID, b.ID)
	})
}

// Devices returns all devices sorted by group and name.
func (c *Core) Devices() []model.Device {
	c.mu.Lock()
	list := make([]model.Device, 0, len(c.devices))
	for _, d := range c.devices {
		list = append(list, d.Clone())
	}
	c.mu.Unlock()
	sortDevices(list)
	return list
}

// Device returns one device or store.ErrNotFound.
func (c *Core) Device(id string) (model.Device, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.devices[id]
	if !ok {
		return model.Device{}, store.ErrNotFound
	}
	return d.Clone(), nil
}

var (
	tokenIDRe = regexp.MustCompile(`^[^\s@!]+@[^\s@!]+![^\s@!]+$`)
	oidRe     = regexp.MustCompile(`^\.?[0-9]+(\.[0-9]+)+$`)
	vmidRe    = regexp.MustCompile(`^[0-9]{1,9}$`)
	hostRe    = regexp.MustCompile(`^[^\s/?#@]+$`)
)

// SaveDevice validates and stores a device (new when ID is empty). Empty secrets keep the
// stored ones; NetCheck IDs are assigned when missing. Field errors are keyed like the JSON
// ("name", "proxmox.token_id", "checks.0.target", "thresholds.1.warn", "rules.0.match").
// Changed check rules apply to the current status at once. A successful save
// schedules an immediate poll. With field errors nothing is stored and the error is nil;
// an unknown ID returns store.ErrNotFound.
func (c *Core) SaveDevice(d model.Device) (model.Device, map[string]string, error) {
	var old *model.Device
	if d.ID != "" {
		o, err := c.Device(d.ID)
		if err != nil {
			return d, nil, err
		}
		old = &o
	}
	errs := c.validateDevice(&d, old)
	if len(errs) > 0 {
		return d, errs, nil
	}
	now := c.Now().Unix()
	if old == nil {
		d.ID, d.Created, d.Maintenance = newID(8), now, nil
	} else {
		d.Created, d.Maintenance = old.Created, old.Maintenance
	}
	d.Updated = now

	c.mu.Lock()
	cur, ok := c.devices[d.ID]
	if old != nil && !ok {
		c.mu.Unlock()
		return d, nil, store.ErrNotFound // deleted meanwhile
	}
	// ParentID and Node change only through Attach, Detach and RegisterAgent.
	d.ParentID, d.Node = cur.ParentID, cur.Node
	if old != nil && d.Type == model.TypeAgent {
		// Facts and LastSeen may have changed since the device was read.
		if cur := c.devices[d.ID]; cur.Agent != nil && d.Agent != nil {
			svc, auto, ign := d.Agent.Services, d.Agent.WatchAutoServices, d.Agent.IgnoreServices
			a := *cur.Agent
			a.Services, a.WatchAutoServices, a.IgnoreServices = svc, auto, ign
			d.Agent = &a
		}
	}
	if err := c.St.Put(KindDevice, d.ID, d); err != nil {
		c.mu.Unlock()
		return d, nil, err
	}
	c.devices[d.ID] = d.Clone()
	rt := c.rt[d.ID]
	if rt == nil {
		rt = newDevState(d.ID)
		c.rt[d.ID] = rt
	}
	rt.next = time.Time{}
	if d.Agent != nil {
		rt.saved = d.Agent.LastSeen
	}
	// Drop the results of removed network checks; thresholds apply from the next poll.
	valid := map[string]bool{}
	for _, nc := range netChecksOf(d) {
		valid[nc.Key()] = true
	}
	checks := make([]model.CheckStatus, 0, len(rt.st.Checks))
	for _, cs := range rt.st.Checks {
		if !strings.HasPrefix(cs.Key, "net:") || valid[cs.Key] {
			checks = append(checks, cs)
		}
	}
	rt.st.Hidden = slices.DeleteFunc(slices.Clone(rt.st.Hidden), func(h model.HiddenCheck) bool {
		return strings.HasPrefix(h.Key, "net:") && !valid[h.Key]
	})
	// Rules apply at once: quiet/alert from the stored verdict, switched-off checks move to
	// Hidden (alerts of both close silently); checks shown again return with the next poll.
	// Inventory: rules decide new checks at once; after a change of what is collected the
	// next collection's unseen checks are expected, not discovered.
	if old != nil && collectChanged(*old, d) {
		rt.acceptUnseen = true
	}
	acceptRuled(d, rt)
	checks = reapplyRules(d, rt, checks, now)
	checks = c.refreshDiscovery(d, rt, checks, now)
	if len(rt.st.Checks) > 0 || old != nil {
		c.finish(d, rt, checks, now)
	}
	c.mu.Unlock()

	if old == nil {
		c.Event("info", d.Name, "Gerät angelegt", fmt.Sprintf("%s (%s) wird überwacht.", d.Name, typeLabel(d.Type)), nil, false)
	} else if d.Type == model.TypeProxmox {
		c.Proxmox.Forget(d.ID)
	}
	c.wakeUp()
	return d, nil, nil
}

// collectChanged reports whether the settings that choose the collected checks changed.
// Values are compared as printed, so a nil and an empty list are the same.
func collectChanged(a, b model.Device) bool {
	show := func(d model.Device) string {
		s := fmt.Sprintf("%s|%+v|%+v|%+v", d.Host, deref(d.Proxmox), deref(d.SNMP), deref(d.NUT))
		if d.Agent != nil {
			s += fmt.Sprintf("|%v|%v|%v", d.Agent.Services, d.Agent.WatchAutoServices, d.Agent.IgnoreServices)
		}
		return s
	}
	return show(a) != show(b)
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func typeLabel(t string) string {
	switch t {
	case model.TypeProxmox:
		return "Proxmox VE"
	case model.TypeAgent:
		return "Agent"
	case model.TypeSNMP:
		return "SNMP"
	case model.TypeNet:
		return "Netzwerk"
	case model.TypeNUT:
		return "USV (NUT)"
	}
	return t
}

// validateDevice normalizes d and returns the field errors. old is the stored device
// (nil for a new one); its secrets fill empty ones.
func (c *Core) validateDevice(d *model.Device, old *model.Device) map[string]string {
	errs := map[string]string{}
	d.Name, d.Host = strings.TrimSpace(d.Name), strings.TrimSpace(d.Host)
	d.Group, d.Notes = strings.TrimSpace(d.Group), strings.TrimSpace(d.Notes)
	switch {
	case d.Name == "":
		errs["name"] = "Name angeben."
	case len([]rune(d.Name)) > 100:
		errs["name"] = "Höchstens 100 Zeichen."
	}
	if len([]rune(d.Group)) > 60 {
		errs["group"] = "Höchstens 60 Zeichen."
	}
	if len([]rune(d.Notes)) > 4000 {
		errs["notes"] = "Höchstens 4000 Zeichen."
	}
	switch d.Type {
	case model.TypeProxmox, model.TypeSNMP, model.TypeNet, model.TypeNUT:
	case model.TypeAgent:
		if old == nil {
			errs["type"] = "Agents werden bei der Installation des Agents automatisch angelegt."
		}
	default:
		errs["type"] = "Typ auswählen."
	}
	if old != nil && old.Type != d.Type {
		errs["type"] = "Der Typ eines Geräts kann nicht geändert werden."
	}
	if d.Host != "" && (len(d.Host) > 253 || !hostRe.MatchString(d.Host)) {
		errs["host"] = "Hostname oder IP-Adresse ohne Protokoll und Pfad angeben."
	} else if d.Host == "" && (d.Type == model.TypeProxmox || d.Type == model.TypeSNMP || d.Type == model.TypeNUT) {
		errs["host"] = "Hostname oder IP-Adresse angeben."
	} else if d.Host == "" && d.Type == model.TypeNet && len(d.Checks) == 0 {
		errs["host"] = "Host oder mindestens einen Check angeben."
	}
	if d.Interval != 0 && (d.Interval < 10 || d.Interval > 3600) {
		errs["interval"] = "0 (Standard) oder 10 bis 3600 Sekunden."
	}

	if d.Type == model.TypeProxmox {
		validateProxmox(d, old, errs)
	} else {
		d.Proxmox = nil
	}
	if d.Type == model.TypeSNMP {
		validateSNMP(d, old, errs)
	} else {
		d.SNMP = nil
	}
	if d.Type == model.TypeNUT {
		validateNUT(d, old, errs)
	} else {
		d.NUT = nil
	}
	if d.Type == model.TypeAgent && old != nil {
		in := d.Agent
		a := model.AgentConfig{}
		if old.Agent != nil {
			a = *old.Agent
		}
		if in != nil {
			a.Services = cleanList(in.Services)
			a.WatchAutoServices = in.WatchAutoServices
			a.IgnoreServices = cleanList(in.IgnoreServices)
		}
		for i, s := range a.Services {
			if len(s) > 256 || strings.ContainsAny(s, "\n\r") {
				errs["agent.services"] = fmt.Sprintf("Dienst %d ist ungültig.", i+1)
			}
		}
		for i, s := range a.IgnoreServices {
			if len(s) > 256 || strings.ContainsAny(s, "\n\r") {
				errs["agent.ignore_services"] = fmt.Sprintf("Eintrag %d ist ungültig.", i+1)
			}
		}
		if len(a.Services) > maxServiceEntries || len(a.IgnoreServices) > maxServiceEntries {
			errs["agent.services"] = fmt.Sprintf("Höchstens %d Einträge.", maxServiceEntries)
		}
		d.Agent = &a
	} else if d.Type != model.TypeAgent {
		d.Agent = nil
	}

	// Network checks: IDs (6 random characters) unique within the device.
	if d.Checks == nil {
		d.Checks = []model.NetCheck{}
	}
	ids := map[string]bool{}
	for i := range d.Checks {
		nc := &d.Checks[i]
		nc.Name, nc.Target = strings.TrimSpace(nc.Name), strings.TrimSpace(nc.Target)
		if nc.ID == "" || ids[nc.ID] || !idRe.MatchString(nc.ID) {
			nc.ID = newID(6)
			for ids[nc.ID] {
				nc.ID = newID(6)
			}
		}
		ids[nc.ID] = true
		for f, msg := range netcheck.Validate(*nc, d.Host) {
			errs[fmt.Sprintf("checks.%d.%s", i, f)] = msg
		}
	}
	if len(d.Checks) > 50 {
		errs["checks"] = "Höchstens 50 Checks pro Gerät."
	}
	if d.Thresholds == nil {
		d.Thresholds = []model.Threshold{}
	}
	validateThresholds(d.Thresholds, errs)
	if d.Rules == nil {
		d.Rules = []model.CheckRule{}
	}
	validateRules(d.Rules, errs)
	c.validateParent(d, old, errs)
	return errs
}

// validateParent rejects a changed ParentID that points to the device itself or to an agent.
// Any other value is ignored: SaveDevice keeps the stored ParentID and Node (they change
// only through Attach, Detach and RegisterAgent).
func (c *Core) validateParent(d *model.Device, old *model.Device, errs map[string]string) {
	d.ParentID = strings.TrimSpace(d.ParentID)
	stored := ""
	if old != nil {
		stored = old.ParentID
	}
	if d.ParentID == "" || d.ParentID == stored {
		return
	}
	if d.ParentID == d.ID {
		errs["parent_id"] = "Ein Gerät kann nicht an sich selbst angehängt werden."
		return
	}
	if p, err := c.Device(d.ParentID); err == nil && p.Type == model.TypeAgent {
		errs["parent_id"] = "Ein Gerät kann nicht an einen Agent angehängt werden."
	}
}

var idRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// cleanList trims entries and drops empty ones and duplicates.
// maxServiceEntries limits the service lists of a device and of the central setting.
const maxServiceEntries = 200

func cleanList(in []string) []string {
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func validateProxmox(d *model.Device, old *model.Device, errs map[string]string) {
	if d.Proxmox == nil {
		d.Proxmox = &model.ProxmoxConfig{}
	}
	p := d.Proxmox
	p.TokenID, p.Fingerprint = strings.TrimSpace(p.TokenID), strings.TrimSpace(p.Fingerprint)
	if p.Secret == "" && old != nil && old.Proxmox != nil {
		p.Secret = old.Proxmox.Secret
	}
	p.Secret = strings.TrimSpace(p.Secret)
	switch {
	case p.TokenID == "":
		errs["proxmox.token_id"] = "Token-ID angeben (z. B. pk95mon@pve!monitoring)."
	case !tokenIDRe.MatchString(p.TokenID):
		errs["proxmox.token_id"] = "Format: benutzer@realm!tokenname"
	}
	if p.Secret == "" {
		errs["proxmox.secret"] = "Secret angeben."
	}
	if p.Port == 0 {
		p.Port = proxmox.DefaultPort
	}
	if p.Port < 1 || p.Port > 65535 {
		errs["proxmox.port"] = "Port zwischen 1 und 65535."
	}
	if p.Fingerprint != "" {
		n := proxmox.NormalizeFingerprint(p.Fingerprint)
		if len(n) != 64 || strings.Trim(n, "0123456789abcdef") != "" {
			errs["proxmox.fingerprint"] = "SHA-256-Fingerabdruck (64 Hex-Zeichen) angeben oder leer lassen."
		} else {
			p.Fingerprint = proxmox.FormatFingerprint(n)
		}
	}
	if p.WatchGuests == "" {
		p.WatchGuests = "onboot"
	}
	if !slices.Contains([]string{"onboot", "all", "none"}, p.WatchGuests) {
		errs["proxmox.watch_guests"] = "Ungültige Auswahl."
	}
	p.IgnoreGuests = cleanList(p.IgnoreGuests)
	for _, id := range p.IgnoreGuests {
		if !vmidRe.MatchString(id) {
			errs["proxmox.ignore_guests"] = "Nur VMIDs (Zahlen) angeben."
		}
	}
}

func validateSNMP(d *model.Device, old *model.Device, errs map[string]string) {
	if d.SNMP == nil {
		d.SNMP = &model.SNMPConfig{}
	}
	s := d.SNMP
	var o model.SNMPConfig
	if old != nil && old.SNMP != nil {
		o = *old.SNMP
	}
	if s.Community == "" {
		s.Community = o.Community
	}
	if s.AuthPass == "" {
		s.AuthPass = o.AuthPass
	}
	if s.PrivPass == "" {
		s.PrivPass = o.PrivPass
	}
	if s.Version == "" {
		s.Version = "2c"
	}
	if s.Port == 0 {
		s.Port = 161
	}
	if s.Port < 1 || s.Port > 65535 {
		errs["snmp.port"] = "Port zwischen 1 und 65535."
	}
	switch s.Version {
	case "1", "2c":
		s.User, s.SecLevel, s.AuthProto, s.AuthPass, s.PrivProto, s.PrivPass = "", "", "", "", "", ""
		if len(s.Community) > 128 {
			errs["snmp.community"] = "Höchstens 128 Zeichen."
		}
	case "3":
		s.Community = ""
		s.User = strings.TrimSpace(s.User)
		if s.User == "" {
			errs["snmp.user"] = "Benutzername angeben."
		}
		if s.SecLevel == "" {
			s.SecLevel = "authPriv"
		}
		auth, priv := false, false
		switch s.SecLevel {
		case "noAuthNoPriv":
		case "authNoPriv":
			auth = true
		case "authPriv":
			auth, priv = true, true
		default:
			errs["snmp.sec_level"] = "Ungültige Sicherheitsstufe."
		}
		if auth {
			if s.AuthProto == "" {
				s.AuthProto = "SHA"
			}
			if !slices.Contains([]string{"MD5", "SHA", "SHA224", "SHA256", "SHA384", "SHA512"}, s.AuthProto) {
				errs["snmp.auth_proto"] = "Ungültiges Protokoll."
			}
			if len(s.AuthPass) < 8 {
				errs["snmp.auth_pass"] = "Passwort mit mindestens 8 Zeichen angeben."
			}
		} else {
			s.AuthProto, s.AuthPass = "", ""
		}
		if priv {
			if s.PrivProto == "" {
				s.PrivProto = "AES"
			}
			if !slices.Contains([]string{"DES", "AES", "AES192", "AES256"}, s.PrivProto) {
				errs["snmp.priv_proto"] = "Ungültiges Protokoll."
			}
			if len(s.PrivPass) < 8 {
				errs["snmp.priv_pass"] = "Passwort mit mindestens 8 Zeichen angeben."
			}
		} else {
			s.PrivProto, s.PrivPass = "", ""
		}
	default:
		errs["snmp.version"] = "Version 1, 2c oder 3 auswählen."
	}
	if s.Profile == "" {
		s.Profile = snmp.ProfileAuto
	}
	if !slices.Contains([]string{snmp.ProfileAuto, snmp.ProfileGeneric, snmp.ProfileSwitch, snmp.ProfileUniFi,
		snmp.ProfileUPS, snmp.ProfilePrinter, snmp.ProfileNAS}, s.Profile) {
		errs["snmp.profile"] = "Ungültiges Profil."
	}
	s.Interfaces = cleanList(s.Interfaces)
	if s.OIDs == nil {
		s.OIDs = []model.CustomOID{}
	}
	names := map[string]bool{}
	for i := range s.OIDs {
		o := &s.OIDs[i]
		o.OID, o.Name, o.Unit = strings.TrimSpace(o.OID), strings.TrimSpace(o.Name), strings.TrimSpace(o.Unit)
		key := func(f string) string { return "snmp.oids." + strconv.Itoa(i) + "." + f }
		if !oidRe.MatchString(o.OID) {
			errs[key("oid")] = "Numerische OID angeben (z. B. 1.3.6.1.4.1.…)."
		}
		switch {
		case o.Name == "":
			errs[key("name")] = "Name angeben."
		case names[o.Name]:
			errs[key("name")] = "Name doppelt."
		}
		names[o.Name] = true
		if len(o.Unit) > 16 {
			errs[key("unit")] = "Höchstens 16 Zeichen."
		}
	}
}

// upsNameRe is a UPS name as upsd lists it (section name in ups.conf).
var upsNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)

func validateNUT(d *model.Device, old *model.Device, errs map[string]string) {
	if d.NUT == nil {
		d.NUT = &model.NUTConfig{}
	}
	n := d.NUT
	if n.Password == "" && old != nil && old.NUT != nil {
		n.Password = old.NUT.Password
	}
	n.UPS, n.Username = strings.TrimSpace(n.UPS), strings.TrimSpace(n.Username)
	if n.Port == 0 {
		n.Port = nut.DefaultPort
	}
	if n.Port < 1 || n.Port > 65535 {
		errs["nut.port"] = "Port zwischen 1 und 65535."
	}
	switch {
	case n.UPS == "":
	case strings.Contains(n.UPS, "@"):
		errs["nut.ups"] = "Nur den UPS-Namen angeben, ohne @Host (der Host steht oben)."
	case !upsNameRe.MatchString(n.UPS):
		errs["nut.ups"] = "Ungültiger UPS-Name (Buchstaben, Ziffern, _ . : -; höchstens 64 Zeichen) – leer lassen für alle USVs."
	}
	switch {
	case len(n.Username) > 64:
		errs["nut.username"] = "Höchstens 64 Zeichen."
	case strings.ContainsAny(n.Username, "\r\n\x00\t "):
		errs["nut.username"] = "Benutzername ohne Leerzeichen und Zeilenumbrüche angeben."
	}
	switch {
	case len(n.Password) > 128:
		errs["nut.password"] = "Höchstens 128 Zeichen."
	case strings.ContainsAny(n.Password, "\r\n\x00"):
		errs["nut.password"] = "Das Passwort darf keinen Zeilenumbruch enthalten."
	case n.Password != "" && n.Username == "":
		errs["nut.username"] = "Benutzername zum Passwort angeben."
	}
}

// DeleteDevice removes a device with its metrics, status and agent token; open alerts are closed.
func (c *Core) DeleteDevice(id string) error {
	c.mu.Lock()
	d, ok := c.devices[id]
	if !ok {
		c.mu.Unlock()
		return store.ErrNotFound
	}
	now := c.Now().Unix()
	c.closeDeviceAlerts(id, now)
	delete(c.devices, id)
	delete(c.rt, id)
	freed := c.detachChildren(id, now) // its attached agents become standalone devices
	c.mu.Unlock()
	if d.Type == model.TypeProxmox {
		c.Proxmox.Forget(id)
	}
	// Wait for writes that saw the device before it was removed (metrics of a running poll,
	// a status or LastSeen being persisted), then delete everything.
	c.pmu.Lock()
	err := c.St.DeleteDeviceData(id)
	if err == nil {
		err = c.St.Delete(KindDevice, id)
	}
	c.pmu.Unlock()
	if err != nil {
		return err
	}
	body := fmt.Sprintf("%s (%s) wird nicht mehr überwacht.", d.Name, typeLabel(d.Type))
	if len(freed) > 0 {
		body += " Angehängte Agents sind jetzt eigenständige Geräte: " + strings.Join(freed, ", ") + "."
	}
	c.Event("info", d.Name, "Gerät gelöscht", body, nil, false)
	return nil
}

// SetMaintenance sets (until > now) or ends (until 0) the maintenance of a device.
func (c *Core) SetMaintenance(id string, until int64, note string) (model.Device, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.devices[id]
	if !ok {
		return model.Device{}, store.ErrNotFound
	}
	now := c.Now().Unix()
	if until > now {
		d.Maintenance = &model.Maintenance{Until: until, Note: strings.TrimSpace(note)}
	} else {
		d.Maintenance = nil
	}
	d.Updated = now
	if err := c.St.Put(KindDevice, id, d); err != nil {
		return d, err
	}
	c.devices[id] = d.Clone()
	if rt := c.rt[id]; rt != nil {
		c.syncAlerts(d, rt, now)
	}
	if d.Maintenance != nil {
		body := "bis " + c.fmtTime(until)
		if d.Maintenance.Note != "" {
			body += " – " + d.Maintenance.Note
		}
		c.Event("info", d.Name, "Wartung begonnen", body, nil, false)
	} else {
		c.Event("info", d.Name, "Wartung beendet", "Alarme sind wieder aktiv.", nil, false)
	}
	return d, nil
}

// ---- connection tests (used by the device dialogs) ----

// TestProxmox tests a Proxmox VE connection. When cfg.Secret is empty and id names an existing
// device, the stored secret is used.
func (c *Core) TestProxmox(ctx context.Context, id, host string, cfg model.ProxmoxConfig) (proxmox.TestResult, error) {
	if cfg.Secret == "" && id != "" {
		if d, err := c.Device(id); err == nil && d.Proxmox != nil {
			cfg.Secret = d.Proxmox.Secret
		}
	}
	return proxmox.Test(ctx, strings.TrimSpace(host), cfg)
}

// TestSNMP tests an SNMP connection (empty secrets taken from device id, as above).
func (c *Core) TestSNMP(ctx context.Context, id, host string, cfg model.SNMPConfig) (snmp.TestResult, error) {
	if id != "" {
		if d, err := c.Device(id); err == nil && d.SNMP != nil {
			if cfg.Community == "" {
				cfg.Community = d.SNMP.Community
			}
			if cfg.AuthPass == "" {
				cfg.AuthPass = d.SNMP.AuthPass
			}
			if cfg.PrivPass == "" {
				cfg.PrivPass = d.SNMP.PrivPass
			}
		}
	}
	return c.SNMP.Test(ctx, strings.TrimSpace(host), cfg)
}

// TestNUT tests a NUT server and lists its UPS. When cfg.Password is empty and id names an
// existing NUT device, the stored password is used.
func (c *Core) TestNUT(ctx context.Context, id, host string, cfg model.NUTConfig) (nut.TestResult, error) {
	if cfg.Password == "" && id != "" {
		if d, err := c.Device(id); err == nil && d.NUT != nil {
			cfg.Password = d.NUT.Password
		}
	}
	cfg.UPS, cfg.Username = strings.TrimSpace(cfg.UPS), strings.TrimSpace(cfg.Username)
	return nut.Test(ctx, strings.TrimSpace(host), cfg)
}

// TestNetCheck runs one network check once.
func (c *Core) TestNetCheck(ctx context.Context, host string, nc model.NetCheck) model.Check {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if nc.ID == "" {
		nc.ID = "test"
	}
	return netcheck.Run(ctx, nc, strings.TrimSpace(host))
}

// ImportDevice stores a device as it is, without validation, keeping its ID (demo mode,
// migrations). Runtime state of an existing device is kept; it is polled soon.
func (c *Core) ImportDevice(d model.Device) error {
	if d.ID == "" {
		return fmt.Errorf("device without id")
	}
	if d.Checks == nil {
		d.Checks = []model.NetCheck{}
	}
	if d.Thresholds == nil {
		d.Thresholds = []model.Threshold{}
	}
	d = d.Clone() // the caller keeps its pointers
	c.mu.Lock()
	if err := c.St.Put(KindDevice, d.ID, d); err != nil {
		c.mu.Unlock()
		return err
	}
	c.devices[d.ID] = d
	rt := c.rt[d.ID]
	if rt == nil {
		rt = newDevState(d.ID)
		c.rt[d.ID] = rt
	}
	rt.next = time.Time{}
	if d.Agent != nil {
		rt.saved = d.Agent.LastSeen
	}
	c.mu.Unlock()
	if d.Type == model.TypeProxmox {
		c.Proxmox.Forget(d.ID)
	}
	c.wakeUp()
	return nil
}
