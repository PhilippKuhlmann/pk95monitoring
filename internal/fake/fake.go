// Package fake is the demo mode (PK95MON_DEMO=1): it wires a simulated Proxmox VE cluster
// (fakepve), simulated SNMP devices (fakesnmp, incl. a UniFi PoE switch), a UniFi UPS on a
// simulated NUT server (fakenut), a local web server for network checks and five simulated
// agents (one attached to the Proxmox cluster) into the engine, and back-fills a week of
// history so the charts have data (also for demo devices added to an existing database).
package fake

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/collect/netcheck"
	"pk95monitoring/internal/collect/snmp"
	"pk95monitoring/internal/core"
	"pk95monitoring/internal/fake/fakegraph"
	"pk95monitoring/internal/fake/fakenut"
	"pk95monitoring/internal/fake/fakeopnsense"
	"pk95monitoring/internal/fake/fakepbs"
	"pk95monitoring/internal/fake/fakepve"
	"pk95monitoring/internal/fake/fakeredfish"
	"pk95monitoring/internal/fake/fakesnmp"
	"pk95monitoring/internal/fake/fakeunifi"
	"pk95monitoring/internal/fake/fakeveeam"
	"pk95monitoring/internal/model"
	"pk95monitoring/internal/store"
)

// Demo device groups (shown in the UI).
const (
	groupServer = "Server"
	groupNet    = "Netzwerk"
	groupPeri   = "Peripherie"
	groupWeb    = "Webdienste"
	groupInfra  = "Infrastruktur"
	groupCloud  = "Cloud"
)

// Simulated SNMP hosts.
const (
	hostSwitch  = "10.0.0.2"
	hostUniFi   = "10.0.0.3"
	hostUPS     = "10.0.0.5"
	hostPrinter = "10.0.0.20"
	hostNAS     = "10.0.0.30"
	hostSophos  = "10.0.0.1"
	hostUTM     = "10.0.0.250"
)

// BackfillDays is the history written on the first start (tests shorten it).
var BackfillDays = 7

// DiscoveryAfter is when the simulated cluster gets a new VM and loses a container.
var DiscoveryAfter = 3 * time.Minute

// ReportEvery is the report interval of the simulated agents.
var ReportEvery = 30 * time.Second

const keyBackfill = "demo.backfill" // settings key: time of the back-fill

// Setup adds the demo devices to c (creating them when missing) and starts the simulations.
// Call it after core.New and before c.Run; stop ends the simulations.
func Setup(c *core.Core) (stop func(), err error) {
	pve := fakepve.New()
	devs := map[string]*fakesnmp.Device{
		hostSwitch:  fakesnmp.NewSwitch(24),
		hostUniFi:   fakesnmp.NewUniFiSwitch(),
		hostUPS:     fakesnmp.NewUPS(),
		hostPrinter: fakesnmp.NewPrinter(),
		hostNAS:     fakesnmp.NewNAS(),
		hostSophos:  fakesnmp.NewSophos(),
		hostUTM:     fakesnmp.NewSecurepoint(),
	}
	fakeDial := fakesnmp.Dialer(devs)
	realDial := c.SNMP.Dial
	if realDial == nil {
		realDial = snmp.Dial
	}
	c.SNMP.Dial = func(ctx context.Context, host string, cfg model.SNMPConfig, timeout time.Duration) (snmp.Session, error) {
		if _, ok := devs[host]; ok {
			return fakeDial(ctx, host, cfg, timeout)
		}
		return realDial(ctx, host, cfg, timeout)
	}

	ups := fakenut.New(map[string]map[string]string{demoUPSName: fakenut.UniFiUPS()})
	ups.SetDesc(demoUPSName, "UniFi UPS 2U")
	ups.EnableDrift()

	vbr := fakeveeam.New()
	pbsSrv := fakepbs.New()
	bmc := fakeredfish.New()
	graph := fakegraph.New()
	udm := fakeunifi.New()
	opn := fakeopnsense.New()
	c.Scanner = demoScan // the demo's networks are simulated
	// The demo's Microsoft 365 tenant is simulated; real tenants added in the demo would be
	// asked there too.
	c.M365.LoginURL, c.M365.GraphURL = graph.URL(), graph.URL()
	web := httptest.NewTLSServer(http.HandlerFunc(demoPage))
	plain := httptest.NewServer(http.HandlerFunc(demoPage))
	closeAll := func() {
		web.Close()
		plain.Close()
		pve.Close()
		ups.Close()
		vbr.Close()
		pbsSrv.Close()
		bmc.Close()
		graph.Close()
		udm.Close()
		opn.Close()
	}

	netcheck.SetRDAPBootstrap(plain.URL + "/rdap/dns.json")
	now := time.Now()
	agents := demoAgents(now)
	if err := createDevices(c, pve, ups, vbr, pbsSrv, bmc, udm, opn, web, plain, agents, now); err != nil {
		closeAll()
		return nil, err
	}
	if err := backfill(c, agents, now); err != nil {
		log.Printf("demo: back-fill: %v", err)
	}
	createWindows(c)
	createStatusPage(c, now)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	report := func() {
		for _, a := range agents {
			if _, err := c.IngestReport(a.id, a.report(c, time.Now())); err != nil && !errors.Is(err, store.ErrNotFound) {
				log.Printf("demo: report %s: %v", a.name, err)
			}
		}
	}
	report()
	wg.Go(func() {
		t := time.NewTicker(ReportEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				report()
			}
		}
	})
	// Discovery: a few minutes in, a new VM appears and a container is deleted, so the
	// device page shows "Neu gefunden" and "Verschwunden".
	wg.Go(func() {
		select {
		case <-ctx.Done():
		case <-time.After(DiscoveryAfter):
			pve.AddGuest(108, "build01", "pve2", "qemu")
			pve.RemoveGuest(203)
		}
	})
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			wg.Wait()
			closeAll()
		})
	}, nil
}

// createWindows adds two maintenance windows on the first demo start.
func createWindows(c *core.Core) {
	if len(c.Windows()) > 0 {
		return
	}
	for _, w := range []core.MaintWindow{
		{Name: "Patchday Server", Enabled: true, Repeat: core.RepeatMonthly, Week: 2, Weekdays: []int{2}, Offset: 2,
			Start: "22:00", Minutes: 240, Folders: []string{groupServer}, Note: "Windows-Updates, Neustarts erlaubt"},
		{Name: "Neustart Netzwerk", Enabled: true, Repeat: core.RepeatWeekly, Weekdays: []int{0}, Start: "04:00", Minutes: 30,
			Folders: []string{groupNet}},
	} {
		if _, errs, err := c.SaveWindow(w); err != nil || len(errs) > 0 {
			log.Printf("demo: window %s: %v %v", w.Name, errs, err)
		}
	}
}

// createStatusPage sets up the status page on the first demo start: services from the demo
// devices, the two maintenance windows announced and a few past problems for the history.
func createStatusPage(c *core.Core, now time.Time) {
	if c.StatusPage().Token != "" {
		return
	}
	p := core.StatusPage{Enabled: true, Title: "IT-Status Muster GmbH",
		Intro: "Bei Störungen erreichen Sie uns unter 0221 123456-0 oder support@muster-it.example.",
		Components: []core.StatusComponent{
			{Name: "Anmeldung und Benutzerkonten", Note: "Windows-Anmeldung, Kennwörter", Mode: core.StatusAll, Devices: []string{"demo-dc01"},
				Checks: []core.StatusCheck{{Group: "Active Directory"}}},
			{Name: "Dateiablage", Note: "Laufwerke S: und P:", Devices: []string{"demo-fs01", "demo-nas"}},
			{Name: "Warenwirtschaft", Note: "ERP und Datenbank", Devices: []string{"demo-hv01", "demo-db01"}},
			{Name: "Webseite", Note: "www.muster-gmbh.com", Devices: []string{"demo-web", "demo-web01"}},
			{Name: "Netzwerk und Internet", Folders: []string{groupNet}},
			{Name: "Drucken", Mode: core.StatusAll, Devices: []string{"demo-printer"}},
		}}
	if _, errs, err := c.SaveStatusPage(p); err != nil || len(errs) > 0 {
		log.Printf("demo: status page: %v %v", errs, err)
		return
	}
	for _, w := range c.Windows() {
		if (w.Name == "Patchday Server" || w.Name == "Neustart Netzwerk") && !w.Public {
			w.MaintWindow.Public = true
			if _, errs, err := c.SaveWindow(w.MaintWindow); err != nil || len(errs) > 0 {
				log.Printf("demo: window %s: %v %v", w.Name, errs, err)
			}
		}
	}
	day := func(d, h, m int) int64 {
		t := now.AddDate(0, 0, -d)
		return time.Date(t.Year(), t.Month(), t.Day(), h, m, 0, 0, t.Location()).Unix()
	}
	for _, a := range []model.Alert{
		{DeviceID: "demo-fs01", DeviceName: "fs01", CheckKey: "reach", CheckName: "Erreichbarkeit", Severity: model.StateCrit,
			Message: "Agent meldet sich nicht", Started: day(5, 14, 10), Ended: day(5, 14, 32)},
		{DeviceID: "demo-web", DeviceName: "Webseite", CheckKey: "net:http01", CheckName: "Startseite", Severity: model.StateCrit,
			Message: "HTTP 503", Started: day(3, 9, 5), Ended: day(3, 9, 50)},
		{DeviceID: "demo-printer", DeviceName: "Drucker Empfang", CheckKey: "snmp:printer", CheckName: "Drucker", Severity: model.StateWarn,
			Message: "Papierstau", Started: day(1, 11, 20), Ended: day(1, 13, 5)},
		{DeviceID: "demo-switch", DeviceName: "Switch Serverraum", CheckKey: "reach", CheckName: "Erreichbarkeit", Severity: model.StateCrit,
			Message: "Keine Antwort", Started: day(2, 6, 0), Ended: day(2, 6, 4)},
	} {
		id, err := c.St.OpenAlert(a)
		if err == nil {
			err = c.St.CloseAlert(id, a.Ended)
		}
		if err != nil {
			log.Printf("demo: alert %s: %v", a.DeviceID, err)
		}
	}
}

// demoPage is the web site of the network checks.
func demoPage(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/rdap/") {
		demoRDAP(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><html lang="de"><head><title>Muster GmbH</title></head>
<body><h1>Willkommen bei der Muster GmbH</h1><p>Demo-Seite von PK95 Monitoring.</p></body></html>`)
}

// demoRDAP is the domain registry of the demo (RDAP): muster-gmbh.com expires in 26 days,
// muster-gmbh.de is registered without a published date (as at DENIC).
func demoRDAP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/rdap+json")
	base := "http://" + r.Host + "/rdap/"
	switch r.URL.Path {
	case "/rdap/dns.json":
		fmt.Fprintf(w, `{"services":[[["com"],[%q]],[["de"],[%q]]]}`, base+"com/", base+"de/")
	case "/rdap/com/domain/muster-gmbh.com":
		exp := time.Now().AddDate(0, 0, 26).Add(2 * time.Hour).UTC().Format(time.RFC3339)
		fmt.Fprintf(w, `{"status":["client transfer prohibited"],"events":[{"eventAction":"registration","eventDate":"2012-03-01T09:00:00Z"},
			{"eventAction":"expiration","eventDate":%q}],"entities":[{"roles":["registrar"],"vcardArray":["vcard",[["fn",{},"text","Demo Registrar AG"]]]}]}`, exp)
	case "/rdap/de/domain/muster-gmbh.de":
		fmt.Fprint(w, `{"status":["active"],"events":[{"eventAction":"last changed","eventDate":"2025-06-01T10:00:00Z"}]}`)
	default:
		http.NotFound(w, r)
	}
}

// demoUPSName is the UPS name on the simulated NUT server.
const demoUPSName = "ups"

// createDevices adds missing demo devices. The Proxmox cluster, the NUT UPS and the web site
// are updated on every start because the simulated servers get new ports and certificates.
func createDevices(c *core.Core, pve *fakepve.Server, ups *fakenut.Server, vbr *fakeveeam.Server, pbsSrv *fakepbs.Server, bmc *fakeredfish.Server, demoUniFi *fakeunifi.Server, demoOPNsense *fakeopnsense.Server, web, plain *httptest.Server, agents []*simAgent, now time.Time) error {
	ts := now.Unix()
	// New demo devices date back to the start of the back-filled history, so reports and the
	// status page history cover it.
	created := now.AddDate(0, 0, -max(BackfillDays, 0)).Unix()
	keep := func(id string, d model.Device) model.Device {
		// Keep what the user changed in the UI (name, group, thresholds …).
		if old, err := c.Device(id); err == nil {
			if old.Proxmox != nil && d.Proxmox != nil {
				d.Proxmox.WatchGuests, d.Proxmox.IgnoreGuests = old.Proxmox.WatchGuests, old.Proxmox.IgnoreGuests
			}
			old.Host, old.Proxmox, old.Updated = d.Host, d.Proxmox, ts
			if d.NUT != nil {
				if old.NUT == nil {
					old.NUT = d.NUT
				}
				old.NUT.Port = d.NUT.Port
			}
			if d.Veeam != nil {
				old.Veeam = d.Veeam // new port and certificate on every start
			}
			if d.PBS != nil {
				old.PBS = d.PBS
			}
			if d.Redfish != nil {
				old.Redfish = d.Redfish
			}
			if d.M365 != nil {
				old.M365 = d.M365
			}
			if d.UniFi != nil {
				old.UniFi = d.UniFi // new port and certificate on every start
			}
			if d.Type == model.TypeNet {
				old.Checks = d.Checks
			}
			return old
		}
		return d
	}

	cfg := pve.Config()
	pd := model.Device{ID: "demo-pve", Name: "Proxmox-Cluster", Type: model.TypeProxmox, Host: pve.Host(),
		Group: groupServer, Enabled: true, Interval: 30, Proxmox: &cfg,
		Notes: "Simulierter Cluster mit drei Knoten (Demo).", Created: created, Updated: ts}
	if err := c.ImportDevice(keep(pd.ID, pd)); err != nil {
		return err
	}

	snmpDev := func(id, name, host, group, profile string) model.Device {
		return model.Device{ID: id, Name: name, Type: model.TypeSNMP, Host: host, Group: group, Enabled: true,
			Interval: 30, SNMP: &model.SNMPConfig{Version: "2c", Port: 161, Community: "public", Profile: profile},
			Created: created, Updated: ts}
	}
	// The switch alarms only when one of its named ports (uplink, servers, NAS, UPS) loses
	// its link; the free ports at the end are down and stay OK ("Kein Link").
	sw := snmpDev("demo-switch", "Switch Serverraum", hostSwitch, groupNet, snmp.ProfileAuto)
	sw.SNMP.Interfaces = slices.Clone(demoLinkPorts)
	// The UniFi switch (profile detected automatically) powers access points, cameras and
	// phones over PoE; it alarms when a server, the NAS, the gateway or an uplink loses its link.
	usw := snmpDev("demo-unifi", "UniFi-Switch Technik", hostUniFi, groupNet, snmp.ProfileAuto)
	usw.SNMP.Interfaces = slices.Clone(demoUniFiLinkPorts)
	usw.Notes = "Simulierter UniFi USW-Pro-24-PoE (Demo): PoE, LLDP-Nachbarn, Lüfter und Temperaturen."
	for _, d := range []model.Device{
		sw,
		usw,
		snmpDev("demo-ups", "USV Serverraum", hostUPS, groupServer, snmp.ProfileAuto),
		snmpDev("demo-printer", "Drucker Empfang", hostPrinter, groupPeri, snmp.ProfileAuto),
		snmpDev("demo-nas", "NAS Sicherung", hostNAS, groupServer, snmp.ProfileAuto),
		withNotes(snmpDev("demo-sophos", "Firewall Zentrale", hostSophos, groupNet, snmp.ProfileAuto),
			"Simulierte Sophos XGS 2100 (Demo, SNMP): HA-Cluster, drei IPsec-Tunnel, Web Protection läuft in 20 Tagen ab."),
		withNotes(snmpDev("demo-securepoint", "Firewall Lager", hostUTM, groupNet, snmp.ProfileAuto),
			"Simulierte Securepoint UTM 14 (Demo, SNMP): Cluster-Master, zwei IPsec-Tunnel mit drei Subnetzpaaren, WireGuard, Lizenz noch 120 Tage."),
	} {
		if err := importOrUpgrade(c, d, ts); err != nil {
			return err
		}
	}

	nd := model.Device{ID: "demo-nut", Name: "USV Technik (UniFi)", Type: model.TypeNUT, Host: ups.Host(), Group: groupInfra,
		Enabled: true, Interval: 30, Created: created, Updated: ts,
		Notes: "Simulierte UniFi UPS 2U am NUT-Server (Demo).",
		NUT:   &model.NUTConfig{Port: ups.Port(), UPS: demoUPSName}}
	if err := c.ImportDevice(keep(nd.ID, nd)); err != nil {
		return err
	}

	vcfg := vbr.Config()
	vd := model.Device{ID: "demo-veeam", Name: "Veeam Backup", Type: model.TypeVeeam, Host: vbr.Host(), Group: groupServer,
		Enabled: true, Interval: 60, Created: created, Updated: ts, Veeam: &vcfg,
		Notes: "Simulierter Veeam Backup & Replication 12 (Demo): Jobs mit Erfolg, Warnung und Fehler, zwei Repositories."}
	if err := c.ImportDevice(keep(vd.ID, vd)); err != nil {
		return err
	}

	pcfg := pbsSrv.Config()
	bd := model.Device{ID: "demo-pbs", Name: "Backup-Server (PBS)", Type: model.TypePBS, Host: pbsSrv.Host(), Group: groupServer,
		Enabled: true, Interval: 60, Created: created, Updated: ts, PBS: &pcfg,
		Notes: "Simulierter Proxmox Backup Server 3 (Demo): zwei Datastores, eine fehlgeschlagene Verifizierung, ein fehlgeschlagener Sync-Job."}
	if err := c.ImportDevice(keep(bd.ID, bd)); err != nil {
		return err
	}

	rcfg := bmc.Config()
	rd := model.Device{ID: "demo-idrac", Name: "iDRAC hv01", Type: model.TypeRedfish, Host: bmc.Host(), Group: groupServer,
		Enabled: true, Interval: 60, Created: created, Updated: ts, Redfish: &rcfg,
		Notes: "Simulierter Dell iDRAC 9 eines PowerEdge R650 (Demo): Netzteil 2 ohne Eingangsstrom, eine SSD mit vorhergesagtem Ausfall."}
	if err := c.ImportDevice(keep(rd.ID, rd)); err != nil {
		return err
	}

	ucfg := demoUniFi.Config()
	ud := model.Device{ID: "demo-udm", Name: "UDM Pro Serverraum", Type: model.TypeUniFi, Host: demoUniFi.Host(), Group: groupNet,
		Enabled: true, Interval: 60, Created: created, Updated: ts, UniFi: &ucfg,
		Notes: "Simulierte UniFi Dream Machine Pro (Demo) mit UniFi Network und Protect: ein Access Point und eine Kamera offline, eine Kamera nimmt nicht auf."}
	if err := c.ImportDevice(keep(ud.ID, ud)); err != nil {
		return err
	}

	ocfg := demoOPNsense.Config()
	od := model.Device{ID: "demo-opnsense", Name: "Firewall Filiale", Type: model.TypeOPNsense, Host: demoOPNsense.Host(), Group: groupNet,
		Enabled: true, Interval: 60, Created: created, Updated: ts, OPNsense: &ocfg,
		Notes: "Simulierte OPNsense 25.7 (Demo) mit zwei WAN-Leitungen, IPsec zur Filiale, WireGuard, CARP und drei ausstehenden Updates."}
	if err := c.ImportDevice(keep(od.ID, od)); err != nil {
		return err
	}

	md := model.Device{ID: "demo-m365", Name: "Microsoft 365", Type: model.TypeM365, Group: groupCloud,
		Enabled: true, Created: created, Updated: ts,
		M365:  &model.M365Config{TenantID: fakegraph.TenantID, ClientID: fakegraph.ClientID, Secret: fakegraph.Secret},
		Notes: "Simulierter Mandant der Muster GmbH (Demo): Entra Connect, eine nicht verifizierte Domain, Entra ID P1 in der Kulanzzeit, eine Copilot-Testversion, ein bald ablaufendes App-Geheimnis und eine Exchange-Störung."}
	if err := c.ImportDevice(keep(md.ID, md)); err != nil {
		return err
	}

	plainAddr := plain.Listener.Addr().(*net.TCPAddr)
	// The test server's certificate is self-signed, so a TLS check (which always verifies the
	// chain) would be red forever. The HTTPS check accepts it and still reports the days left
	// (cert.days) and the issuer, which is what the demo should show.
	wd := model.Device{ID: "demo-web", Name: "Webseite", Type: model.TypeNet, Host: "127.0.0.1", Group: groupWeb,
		Enabled: true, Interval: 30, Created: created, Updated: ts,
		Notes: "Prüft einen lokalen Test-Webserver (Demo): Startseite mit Suchbegriff, Zertifikatslaufzeit, HTTP-Port, Ping und Domain-Ablauf.",
		Checks: []model.NetCheck{
			{ID: "http01", Type: model.NetHTTP, Name: "Startseite", Target: web.URL + "/", Keyword: "Willkommen", IgnoreTLS: true},
			{ID: "tcp001", Type: model.NetTCP, Name: "HTTP-Port", Target: plainAddr.String()},
			{ID: "ping01", Type: model.NetPing, Name: "Ping", Target: "127.0.0.1"},
			{ID: "dom001", Type: model.NetDomain, Target: "www.muster-gmbh.com"},
			{ID: "dom002", Type: model.NetDomain, Target: "muster-gmbh.de"},
		}}
	if err := c.ImportDevice(keep(wd.ID, wd)); err != nil {
		return err
	}

	for _, a := range agents {
		if err := importOrUpgrade(c, a.device(created, ts), ts); err != nil {
			return err
		}
	}
	return nil
}

// demoLinkPorts are the switch ports with "Alarm bei Link-Ausfall" (fakesnmp names; the
// ports with an alias: router uplink, pve1–3, NAS, UPS).
var demoLinkPorts = []string{"Gi1/0/1", "Gi1/0/2", "Gi1/0/3", "Gi1/0/4", "Gi1/0/5", "Gi1/0/6"}

// demoUniFiLinkPorts are the UniFi switch ports with "Alarm bei Link-Ausfall": pve1, pve2,
// NAS, gateway and the two SFP+ uplinks.
var demoUniFiLinkPorts = []string{"0/13", "0/14", "0/15", "0/24", "0/25", "0/26"}

// importOrUpgrade creates a missing demo device. An existing one keeps what the user changed
// and only gets demo fields it never had (nil: the UI stores [] when they are cleared).
func importOrUpgrade(c *core.Core, d model.Device, ts int64) error {
	old, err := c.Device(d.ID)
	if err != nil {
		return c.ImportDevice(d)
	}
	changed := false
	if old.Rules == nil && len(d.Rules) > 0 {
		old.Rules, changed = slices.Clone(d.Rules), true
	}
	if old.SNMP != nil && d.SNMP != nil && old.SNMP.Interfaces == nil && len(d.SNMP.Interfaces) > 0 {
		old.SNMP.Interfaces, changed = slices.Clone(d.SNMP.Interfaces), true
	}
	if !changed {
		return nil
	}
	old.Updated = ts
	return c.ImportDevice(old)
}

// withNotes sets the notes of a demo device.
func withNotes(d model.Device, notes string) model.Device {
	d.Notes = notes
	return d
}
