package fake

import (
	"context"
	"time"

	"pk95monitoring/internal/netscan"
)

// demoNetwork is what a scan of the demo's networks finds: the demo servers (already
// monitored through their agents) and devices that are not monitored yet.
var demoNetwork = map[string]netscan.Host{
	"10.0.0.1": {Name: "fw.kunde.local", Ports: []int{netscan.PortDNS, netscan.PortHTTPS}, Title: "Sophos Firewall",
		SNMP: &netscan.SNMPInfo{Descr: "Sophos XGS 2100 SFOS 21.0.1", ObjectID: "1.3.6.1.4.1.2604.5", Name: "fw"}},
	"10.0.0.11": {Name: "dc01.kunde.local", Ping: true, Ports: []int{netscan.PortDNS, netscan.PortSMB, netscan.PortRDP, netscan.PortWinRM}},
	"10.0.0.12": {Name: "fs01.kunde.local", Ping: true, Ports: []int{netscan.PortSMB, netscan.PortRDP, netscan.PortWinRM}},
	"10.0.0.15": {Name: "hv01.kunde.local", Ping: true, Ports: []int{netscan.PortSMB, netscan.PortRDP, netscan.PortWinRM}},
	"10.0.0.16": {Name: "sql01.kunde.local", Ping: true, Ports: []int{netscan.PortSMB, netscan.PortMSSQL, netscan.PortRDP, netscan.PortWinRM}},
	"10.0.0.21": {Name: "web01.kunde.local", Ping: true, Ports: []int{netscan.PortSSH, netscan.PortHTTP, netscan.PortHTTPS}, Title: "Muster GmbH"},
	"10.0.0.22": {Name: "db01.kunde.local", Ping: true, Ports: []int{netscan.PortSSH}},
	"10.0.0.25": {Name: "ilo-srv02.kunde.local", Ping: true, Ports: []int{netscan.PortHTTP, netscan.PortHTTPS}, Redfish: "HPE iLO 6",
		Title: "iLO: ilo-srv02", Cert: "ilo-srv02, ausgestellt von Hewlett Packard Enterprise"},
	"10.0.0.2": {Name: "sw-serverraum.kunde.local", Ping: true, Ports: []int{netscan.PortSSH, netscan.PortHTTPS},
		SNMP: &netscan.SNMPInfo{Descr: "HP J9772A 2530-48G-PoE+ Switch, revision YA.16.11.0012", ObjectID: "1.3.6.1.4.1.11.2.3.7.11.155"}},
	"10.0.0.3": {Name: "usw-technik.kunde.local", Ping: true, Ports: []int{netscan.PortSSH},
		SNMP: &netscan.SNMPInfo{Descr: "UniFi USW-Pro-24-PoE, 7.1.26", ObjectID: "1.3.6.1.4.1.4413"}},
	"10.0.0.5": {Name: "usv-serverraum.kunde.local", Ping: true, Ports: []int{netscan.PortHTTP},
		SNMP: &netscan.SNMPInfo{Descr: "APC Web/SNMP Management Card (MB:v4.1.0 PF:v6.9.6 PN:apc_hw05_aos_696.bin)", ObjectID: "1.3.6.1.4.1.318.1.3.27"}},
	"10.0.0.20": {Name: "drucker-empfang.kunde.local", Ping: true, Ports: []int{netscan.PortHTTP, netscan.PortIPP, netscan.PortJetDir},
		SNMP: &netscan.SNMPInfo{Descr: "HP LaserJet Pro M404dn", ObjectID: "1.3.6.1.4.1.11.2.3.9.1"}},
	"10.0.0.30": {Name: "nas-sicherung.kunde.local", Ping: true, Ports: []int{netscan.PortSSH, netscan.PortHTTPS, netscan.PortSynology},
		SNMP: &netscan.SNMPInfo{Descr: "Linux DS920plus 5.10.55+ #72806 SMP", ObjectID: "1.3.6.1.4.1.6574.1"}},
	"10.0.0.35": {Name: "drucker-buero.kunde.local", Ping: true, Ports: []int{netscan.PortHTTP, netscan.PortIPP, netscan.PortJetDir},
		SNMP: &netscan.SNMPInfo{Descr: "KYOCERA Document Solutions Printing System ECOSYS M5526cdn", ObjectID: "1.3.6.1.4.1.1347.41"}},
	"10.0.0.31": {Name: "drucker-lager.kunde.local", Ping: true, Ports: []int{netscan.PortHTTP, netscan.PortIPP, netscan.PortJetDir}, Title: "Brother HL-L2350DW"},
	"10.0.0.40": {Name: "pc-empfang.kunde.local", Ping: true, Ports: []int{netscan.PortSMB, netscan.PortRDP}},
	"10.0.0.41": {Name: "srv-dms.kunde.local", Ping: true, Ports: []int{netscan.PortSMB, netscan.PortMSSQL, netscan.PortRDP, netscan.PortWinRM}},
	"10.0.0.50": {Name: "nas-archiv.kunde.local", Ping: true, Ports: []int{netscan.PortHTTP, netscan.PortHTTPS, netscan.PortSynology}, Title: "nas-archiv - Synology DiskStation"},
	"10.0.0.60": {Ping: true, Ports: []int{netscan.PortHTTP},
		SNMP: &netscan.SNMPInfo{Descr: "APC Web/SNMP Management Card (MB:v4.1.0 PF:v6.9.6 PN:apc_hw05_aos_696.bin)", ObjectID: "1.3.6.1.4.1.318.1.3.27", Name: "usv-buero"}},
	"10.0.0.70":  {Name: "unifi.kunde.local", Ping: true, Ports: []int{netscan.PortSSH, netscan.PortUniFi}, Title: "UniFi Network"},
	"10.0.0.80":  {Name: "backup-pi.kunde.local", Ping: true, Ports: []int{netscan.PortSSH}},
	"10.0.0.90":  {Ping: true},
	"10.0.10.11": {Name: "pve1.kunde.local", Ping: true, Ports: []int{netscan.PortSSH, netscan.PortPVE}, Title: "pve1 - Proxmox Virtual Environment"},
}

// demoScan simulates a scan of demoNetwork; it takes a few seconds so the progress shows.
func demoScan(ctx context.Context, o netscan.Options) []netscan.Host {
	var out []netscan.Host
	step := 4 * time.Second / time.Duration(max(len(o.Addrs), 1))
	for i, a := range o.Addrs {
		select {
		case <-ctx.Done():
			return out
		case <-time.After(step):
		}
		if h, ok := demoNetwork[a.String()]; ok {
			h.IP = a.String()
			if h.Ports == nil {
				h.Ports = []int{}
			}
			if h.Name == "" && h.SNMP != nil {
				h.Name = h.SNMP.Name
			}
			netscan.Classify(&h)
			out = append(out, h)
		}
		if o.Progress != nil {
			o.Progress(i+1, len(o.Addrs))
		}
	}
	return out
}
