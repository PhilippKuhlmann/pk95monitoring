package core

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"pk95monitoring/internal/model"
)

// Ready-made services for the status page. A template finds the devices it needs (domain
// controllers, Microsoft 365 tenants, printers) or creates the monitoring for it (a network
// device that checks the internet connection or a web site) and adds the service to the page.

// Template IDs.
const (
	TplInternet = "internet"
	TplWebsite  = "website"
	TplLogon    = "logon"
	TplM365     = "m365"
	TplPrinting = "printing"
	TplVideo    = "video"
)

// Targets of the internet device: two anycast resolvers of different providers, a DNS lookup
// and Microsoft's connectivity test page (what Windows uses to detect internet access).
const (
	inetPing1 = "1.1.1.1"
	inetPing2 = "8.8.8.8"
	inetTest  = "http://www.msftconnecttest.com/connecttest.txt"
)

// StatusTemplate is a ready-made service as the UI lists it.
type StatusTemplate struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Text  string `json:"text"`
	Hint  string `json:"hint"`  // what applying finds or creates
	Ready bool   `json:"ready"` // false: nothing to build the service from yet
	Input string `json:"input,omitempty"`
}

// templateFind lists the devices a template uses. Caller holds c.mu.
func (c *Core) templateFind(id string) []model.Device {
	var out []model.Device
	for _, d := range c.devices {
		if !d.Enabled {
			continue
		}
		rt := c.rt[d.ID]
		has := func(key string) bool {
			if rt == nil {
				return false
			}
			_, ok := rt.check(key)
			return ok
		}
		switch id {
		case TplInternet:
			if d.Type == model.TypeNet && hasPing(d, inetPing1) && hasPing(d, inetPing2) {
				out = append(out, d)
			}
		case TplLogon:
			if has("ad:dc") {
				out = append(out, d)
			}
		case TplM365:
			if d.Type == model.TypeM365 {
				out = append(out, d)
			}
		case TplPrinting:
			if has("snmp:printer") {
				out = append(out, d)
			}
		case TplVideo:
			if rt != nil && slices.ContainsFunc(rt.st.Checks, func(cs model.CheckStatus) bool { return strings.HasPrefix(cs.Key, "unifi:cam:") }) {
				out = append(out, d)
			}
		}
	}
	slices.SortFunc(out, func(a, b model.Device) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

func hasPing(d model.Device, target string) bool {
	return slices.ContainsFunc(d.Checks, func(n model.NetCheck) bool { return n.Type == model.NetPing && n.Target == target })
}

func deviceNames(list []model.Device, limit int) string {
	names := make([]string, 0, len(list))
	for i, d := range list {
		if i == limit {
			names = append(names, "…")
			break
		}
		names = append(names, d.Name)
	}
	return strings.Join(names, ", ")
}

// StatusTemplates lists the templates with what they would use right now.
func (c *Core) StatusTemplates() []StatusTemplate {
	c.mu.Lock()
	defer c.mu.Unlock()
	found := func(id, one, many, none string) (string, bool) {
		l := c.templateFind(id)
		switch len(l) {
		case 0:
			return none, false
		case 1:
			return fmt.Sprintf("%s: %s", one, l[0].Name), true
		}
		return fmt.Sprintf("%d %s: %s", len(l), many, deviceNames(l, 4)), true
	}
	out := []StatusTemplate{}
	inet := StatusTemplate{ID: TplInternet, Name: "Internet", Ready: true,
		Text: "Ob der Internetzugang funktioniert – geprüft von hier aus nach draußen."}
	if l := c.templateFind(TplInternet); len(l) > 0 {
		inet.Hint = "Nutzt das vorhandene Gerät „" + l[0].Name + "“."
	} else {
		inet.Hint = "Legt das Gerät „Internet“ an: Ping zu 1.1.1.1 und 8.8.8.8, eine DNS-Auflösung und die Microsoft-Testseite. Fällt nur ein Teil aus, heißt es „Teilweise gestört“."
	}
	out = append(out, inet)
	out = append(out, StatusTemplate{ID: TplWebsite, Name: "Webseite", Ready: true, Input: "url",
		Text: "Ob eine Webseite erreichbar ist, z. B. die Firmenseite oder der Online-Shop.",
		Hint: "Fragt nach der Adresse und legt dafür ein Gerät mit HTTPS-Prüfung und Domain-Ablauf an (oder nutzt ein vorhandenes)."})
	logon := StatusTemplate{ID: TplLogon, Name: "Windows-Anmeldung",
		Text: "Anmeldung, Kennwörter und Gruppenrichtlinien – die Active-Directory-Checks der Domänencontroller."}
	logon.Hint, logon.Ready = found(TplLogon, "Domänencontroller", "Domänencontroller", "Kein Domänencontroller gefunden – braucht den Agent auf einem Domänencontroller.")
	out = append(out, logon)
	m365 := StatusTemplate{ID: TplM365, Name: "Microsoft 365", Text: "E-Mail, Teams, OneDrive und SharePoint – nach dem Dienststatus von Microsoft."}
	m365.Hint, m365.Ready = found(TplM365, "Mandant", "Mandanten", "Noch kein Microsoft-365-Mandant – erst unter Geräte → Hinzufügen einrichten.")
	out = append(out, m365)
	pr := StatusTemplate{ID: TplPrinting, Name: "Drucken", Text: "Ob die Drucker erreichbar sind (Toner und Papier zählen nicht)."}
	pr.Hint, pr.Ready = found(TplPrinting, "Drucker", "Drucker", "Kein Drucker gefunden – Drucker werden per SNMP überwacht.")
	out = append(out, pr)
	vid := StatusTemplate{ID: TplVideo, Name: "Videoüberwachung", Text: "Kameras und Aufzeichnung von UniFi Protect – fällt ein Teil der Kameras aus, heißt es „Teilweise gestört“."}
	vid.Hint, vid.Ready = found(TplVideo, "UniFi-Konsole", "UniFi-Konsolen", "Keine UniFi-Konsole mit Protect-Kameras – erst unter Geräte → Hinzufügen einrichten.")
	out = append(out, vid)
	return out
}

// ApplyStatusTemplate adds the service of a template to the status page, creating its
// device first when needed. input is the address for TplWebsite. It returns the saved page
// and a German summary; field errors are keyed "url" or "template".
func (c *Core) ApplyStatusTemplate(id, input string) (StatusPage, string, map[string]string, error) {
	var (
		cp      StatusComponent
		created string
	)
	ensure := func(find func() (model.Device, bool), d model.Device) (model.Device, map[string]string, error) {
		if old, ok := find(); ok {
			return old, nil, nil
		}
		nd, errs, err := c.SaveDevice(d)
		if err == nil && len(errs) == 0 {
			created = nd.Name
		}
		return nd, errs, err
	}
	switch id {
	case TplInternet:
		d, errs, err := ensure(func() (model.Device, bool) {
			c.mu.Lock()
			defer c.mu.Unlock()
			l := c.templateFind(TplInternet)
			if len(l) == 0 {
				return model.Device{}, false
			}
			return l[0], true
		}, model.Device{Name: "Internet", Type: model.TypeNet, Group: "Netzwerk", Enabled: true,
			Notes: "Für die Statusseite angelegt: prüft von hier aus den Weg ins Internet.",
			Checks: []model.NetCheck{
				{Type: model.NetPing, Name: "Cloudflare (1.1.1.1)", Target: inetPing1},
				{Type: model.NetPing, Name: "Google (8.8.8.8)", Target: inetPing2},
				{Type: model.NetDNS, Name: "DNS-Auflösung", Target: "www.microsoft.com"},
				{Type: model.NetHTTP, Name: "Microsoft-Testseite", Target: inetTest, Keyword: "Microsoft Connect Test"},
			}})
		if err != nil || len(errs) > 0 {
			return StatusPage{}, "", errs, err
		}
		cp = StatusComponent{Name: "Internet", Note: "Internetzugang", Mode: StatusOutage, Devices: []string{d.ID},
			Checks: []StatusCheck{{Device: d.ID, Key: "net:*"}}}
	case TplWebsite:
		u, host, msg := websiteURL(input)
		if msg != "" {
			return StatusPage{}, "", map[string]string{"url": msg}, nil
		}
		d, errs, err := ensure(func() (model.Device, bool) {
			c.mu.Lock()
			defer c.mu.Unlock()
			for _, d := range c.devices {
				if slices.ContainsFunc(d.Checks, func(n model.NetCheck) bool { return n.Type == model.NetHTTP && n.Target == u }) {
					return d, true
				}
			}
			return model.Device{}, false
		}, model.Device{Name: "Webseite " + host, Type: model.TypeNet, Group: "Webseiten", Enabled: true,
			Notes: "Für die Statusseite angelegt.",
			Checks: []model.NetCheck{
				{Type: model.NetHTTP, Name: "Startseite", Target: u},
				{Type: model.NetDomain, Name: "Domain-Ablauf", Target: strings.TrimPrefix(host, "www.")},
			}})
		if err != nil {
			return StatusPage{}, "", nil, err
		}
		if len(errs) > 0 {
			return StatusPage{}, "", map[string]string{"url": "Die Adresse lässt sich nicht überwachen."}, nil
		}
		i := slices.IndexFunc(d.Checks, func(n model.NetCheck) bool { return n.Type == model.NetHTTP && n.Target == u })
		if i < 0 {
			return StatusPage{}, "", map[string]string{"url": "Keine HTTP-Prüfung für diese Adresse gefunden."}, nil
		}
		cp = StatusComponent{Name: "Webseite", Note: host, Mode: StatusOutage, Devices: []string{d.ID},
			Checks: []StatusCheck{{Device: d.ID, Key: d.Checks[i].Key()}}}
	case TplLogon, TplM365, TplPrinting, TplVideo:
		c.mu.Lock()
		l := c.templateFind(id)
		c.mu.Unlock()
		if len(l) == 0 {
			return StatusPage{}, "", map[string]string{"template": "Dafür gibt es noch keine passenden Geräte."}, nil
		}
		ids := make([]string, 0, len(l))
		for _, d := range l {
			ids = append(ids, d.ID)
		}
		switch id {
		case TplLogon:
			cp = StatusComponent{Name: "Windows-Anmeldung", Note: "Anmeldung, Kennwörter und Gruppenrichtlinien", Mode: StatusOutage,
				Devices: ids, Checks: []StatusCheck{{Group: "Active Directory"}}}
		case TplM365:
			cp = StatusComponent{Name: "Microsoft 365", Note: "E-Mail, Teams, OneDrive und SharePoint", Mode: StatusAll,
				Devices: ids, Checks: []StatusCheck{{Key: "m365:health"}}}
		case TplPrinting:
			cp = StatusComponent{Name: "Drucken", Mode: StatusOutage, Devices: ids}
		case TplVideo:
			cp = StatusComponent{Name: "Videoüberwachung", Note: "Kameras und Aufzeichnung", Mode: StatusOutage, Devices: ids,
				Checks: []StatusCheck{{Group: "Kameras"}, {Group: "UniFi Protect"}}}
		}
	default:
		return StatusPage{}, "", map[string]string{"template": "Unbekannte Vorlage."}, nil
	}

	p := c.StatusPage()
	if slices.ContainsFunc(p.Components, func(x StatusComponent) bool { return strings.EqualFold(x.Name, cp.Name) && x.Note == cp.Note }) {
		return StatusPage{}, "", map[string]string{"template": fmt.Sprintf("Den Dienst „%s“ gibt es schon.", cp.Name)}, nil
	}
	p.Components = append(p.Components, cp)
	saved, errs, err := c.SaveStatusPage(p)
	if err != nil || len(errs) > 0 {
		return StatusPage{}, "", errs, err
	}
	msg := fmt.Sprintf("Dienst „%s“ angelegt.", cp.Name)
	if created != "" {
		msg += fmt.Sprintf(" Neues Gerät „%s“ – die erste Prüfung läuft.", created)
	}
	return saved, msg, nil, nil
}

// websiteURL normalizes the address of a web site ("muster.de" → "https://muster.de/") and
// returns its host, or a German error.
func websiteURL(in string) (string, string, string) {
	s := strings.TrimSpace(in)
	if s == "" {
		return "", "", "Adresse der Webseite angeben, z. B. https://www.muster-gmbh.de."
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || !strings.Contains(u.Hostname(), ".") || u.User != nil {
		return "", "", "Adresse mit http:// oder https:// angeben, z. B. https://www.muster-gmbh.de."
	}
	if u.Path == "" {
		u.Path = "/"
	}
	u.Fragment = ""
	return u.String(), strings.ToLower(u.Hostname()), ""
}

// StatusCatalogDevice is a device with its checks, for choosing the checks of a service.
type StatusCatalogDevice struct {
	ID     string               `json:"id"`
	Name   string               `json:"name"`
	Group  string               `json:"group,omitempty"`
	Checks []StatusCatalogCheck `json:"checks"`
}

// StatusCatalogCheck is one check of a StatusCatalogDevice.
type StatusCatalogCheck struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Group string `json:"group"`
	State string `json:"state"`
}

// StatusCatalog lists the enabled devices a service with these devices and folders would
// consume (attached agents included) with their checks, by name. The reachability and the
// inventory check are left out: an unreachable device always counts.
func (c *Core) StatusCatalog(devices, folders []string) []StatusCatalogDevice {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []StatusCatalogDevice{}
	for _, d := range c.devices {
		if !d.Enabled || !c.targetMatches(devices, folders, d) {
			continue
		}
		cd := StatusCatalogDevice{ID: d.ID, Name: c.displayName(d), Group: d.Group, Checks: []StatusCatalogCheck{}}
		if rt := c.rt[d.ID]; rt != nil {
			for _, cs := range rt.st.Checks {
				if cs.Key == reachKey || cs.Key == model.DiscoveryKey {
					continue
				}
				cd.Checks = append(cd.Checks, StatusCatalogCheck{Key: cs.Key, Name: cs.Name, Group: cs.Group, State: cs.State})
			}
		}
		slices.SortFunc(cd.Checks, func(a, b StatusCatalogCheck) int {
			if r := strings.Compare(a.Group, b.Group); r != 0 {
				return r
			}
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		})
		out = append(out, cd)
	}
	slices.SortFunc(out, func(a, b StatusCatalogDevice) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out
}
