// Package netscan searches IP ranges for devices worth monitoring: it pings every address,
// tries the TCP ports of the services the monitoring knows (Proxmox, Veeam, Windows, SSH,
// printers …), asks SNMP v2c with a community (usually "public") for sysDescr, reads the
// title and certificate of web interfaces and the Redfish service root of management
// controllers, and classifies each host with a suggestion how to monitor it.
package netscan

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gosnmp/gosnmp"
	probing "github.com/prometheus-community/pro-bing"

	"pk95monitoring/internal/collect/snmp"
	"pk95monitoring/internal/model"
)

// Limits of one scan.
const (
	MaxAddresses = 4096 // all ranges together
	MinPrefix    = 22   // a range may be at most a /22
	workers      = 64
)

// Port numbers the scanner tries and what they stand for.
const (
	PortSSH      = 22
	PortDNS      = 53
	PortHTTP     = 80
	PortHTTPS    = 443
	PortSMB      = 445
	PortIPP      = 631
	PortMSSQL    = 1433
	PortRDP      = 3389
	PortNUT      = 3493
	PortSynology = 5001
	PortWinRM    = 5985
	PortPVE      = 8006
	PortPBS      = 8007
	PortUniFi    = 8443
	PortJetDir   = 9100
	PortVeeam    = 9419
)

// Ports are the TCP ports scanned by default.
var Ports = []int{PortSSH, PortDNS, PortHTTP, PortHTTPS, PortSMB, PortIPP, PortMSSQL, PortRDP, PortNUT, PortSynology,
	PortWinRM, PortPVE, PortPBS, PortUniFi, PortJetDir, PortVeeam}

// SNMPInfo is the system group of a host that answered SNMP.
type SNMPInfo struct {
	Descr    string `json:"descr"`
	ObjectID string `json:"object_id"`
	Name     string `json:"name"`
}

// Host is a found host with what it offers and the suggestion.
type Host struct {
	IP      string    `json:"ip"`
	Name    string    `json:"name,omitempty"` // reverse DNS or sysName
	Ping    bool      `json:"ping"`
	Ports   []int     `json:"ports"`
	SNMP    *SNMPInfo `json:"snmp,omitempty"`
	Title   string    `json:"title,omitempty"`  // title of the web interface
	Cert    string    `json:"cert,omitempty"`   // subject and issuer of its certificate
	Server  string    `json:"server,omitempty"` // HTTP Server header
	Redfish string    `json:"redfish,omitempty"`
	Kind    string    `json:"kind"`  // see Kind* constants
	Label   string    `json:"label"` // German: what it is
	Detail  string    `json:"detail,omitempty"`
	// Suggest is the device type to add (model.Type*), "agent" (install the agent) or "".
	Suggest  string `json:"suggest"`
	Profile  string `json:"profile,omitempty"` // SNMP profile when Suggest is snmp
	Port     int    `json:"port,omitempty"`    // port for the device dialog
	SuggestT string `json:"suggest_text"`      // German: what to do
}

// Kinds of hosts.
const (
	KindProxmox  = "proxmox"
	KindPBS      = "pbs"
	KindVeeam    = "veeam"
	KindRedfish  = "redfish"
	KindNUT      = "nut"
	KindPrinter  = "printer"
	KindUPS      = "ups"
	KindNAS      = "nas"
	KindSwitch   = "switch"
	KindUniFi    = "unifi"
	KindUniFiOS  = "unifios" // UniFi OS console (Dream Machine, Cloud Key, UNVR)
	KindSNMP     = "snmp"
	KindWindows  = "windows"
	KindLinux    = "linux"
	KindWeb      = "web"
	KindPingOnly = "ping"
)

// Options configure a scan. The probe functions default to the real network; tests replace
// them.
type Options struct {
	Addrs     []netip.Addr
	Ports     []int
	Community string // SNMP v2c community; "" = no SNMP
	Timeout   time.Duration
	Progress  func(done, total int) // called concurrently from the workers

	Ping   func(ctx context.Context, ip netip.Addr) bool
	Dial   func(ctx context.Context, addr string) (net.Conn, error)
	SNMP   func(ctx context.Context, ip netip.Addr, community string) (*SNMPInfo, bool)
	HTTP   *http.Client
	Lookup func(ctx context.Context, ip netip.Addr) string
}

// Expand turns ranges ("10.0.0.0/24", "192.168.1.10", "10.0.0.20-10.0.0.40") into addresses
// (a prefix without its network and broadcast address) and the normalized ranges. Errors
// are German and name the range.
func Expand(ranges []string) ([]netip.Addr, []string, error) {
	var out []netip.Addr
	var norm []string
	seen := map[netip.Addr]bool{}
	add := func(a netip.Addr) error {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
		if len(out) > MaxAddresses {
			return fmt.Errorf("zu viele Adressen – höchstens %d insgesamt", MaxAddresses)
		}
		return nil
	}
	for _, r := range ranges {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		bad := func(msg string) error { return fmt.Errorf("„%s“: %s", r, msg) }
		switch {
		case strings.Contains(r, "/"):
			p, err := netip.ParsePrefix(r)
			if err != nil || !p.Addr().Is4() {
				return nil, nil, bad("kein IPv4-Netz wie 192.168.1.0/24")
			}
			if p.Bits() < MinPrefix {
				return nil, nil, bad(fmt.Sprintf("höchstens /%d (%d Adressen)", MinPrefix, 1<<(32-MinPrefix)))
			}
			p = p.Masked()
			first, last := p.Addr(), lastAddr(p)
			for a := first; a.IsValid() && p.Contains(a); a = a.Next() {
				if p.Bits() <= 30 && (a == first || a == last) {
					continue
				}
				if err := add(a); err != nil {
					return nil, nil, bad(err.Error())
				}
			}
			norm = append(norm, p.String())
		case strings.Contains(r, "-"):
			f, l, _ := strings.Cut(r, "-")
			a, err1 := netip.ParseAddr(strings.TrimSpace(f))
			b, err2 := netip.ParseAddr(strings.TrimSpace(l))
			if err1 != nil || err2 != nil || !a.Is4() || !b.Is4() || b.Less(a) {
				return nil, nil, bad("Bereich wie 192.168.1.10-192.168.1.50")
			}
			for x := a; ; x = x.Next() {
				if err := add(x); err != nil {
					return nil, nil, bad(err.Error())
				}
				if x == b {
					break
				}
			}
			norm = append(norm, a.String()+"-"+b.String())
		default:
			a, err := netip.ParseAddr(r)
			if err != nil || !a.Is4() {
				return nil, nil, bad("keine IPv4-Adresse")
			}
			if err := add(a); err != nil {
				return nil, nil, bad(err.Error())
			}
			norm = append(norm, a.String())
		}
	}
	return out, norm, nil
}

func lastAddr(p netip.Prefix) netip.Addr {
	a := p.Addr().As4()
	n := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	n |= (1 << (32 - p.Bits())) - 1
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}

// Scan probes every address and returns the hosts that answered, sorted by address.
func Scan(ctx context.Context, o Options) []Host {
	o = withDefaults(o)
	var (
		mu    sync.Mutex
		hosts []Host
		done  atomic.Int64
		wg    sync.WaitGroup
	)
	jobs := make(chan netip.Addr)
	for range min(workers, max(len(o.Addrs), 1)) {
		wg.Go(func() {
			for ip := range jobs {
				if h, ok := probe(ctx, o, ip); ok {
					mu.Lock()
					hosts = append(hosts, h)
					mu.Unlock()
				}
				n := done.Add(1)
				if o.Progress != nil {
					o.Progress(int(n), len(o.Addrs))
				}
			}
		})
	}
feed:
	for _, ip := range o.Addrs {
		select {
		case jobs <- ip:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	slices.SortFunc(hosts, func(a, b Host) int {
		x, _ := netip.ParseAddr(a.IP)
		y, _ := netip.ParseAddr(b.IP)
		return x.Compare(y)
	})
	return hosts
}

func withDefaults(o Options) Options {
	if o.Timeout <= 0 {
		o.Timeout = 1500 * time.Millisecond
	}
	if o.Ports == nil {
		o.Ports = Ports
	}
	timeout := o.Timeout
	if o.Ping == nil {
		o.Ping = func(ctx context.Context, ip netip.Addr) bool { return pingHost(ctx, ip, timeout) }
	}
	if o.Dial == nil {
		d := &net.Dialer{Timeout: timeout}
		o.Dial = func(ctx context.Context, addr string) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) }
	}
	if o.SNMP == nil {
		o.SNMP = func(ctx context.Context, ip netip.Addr, community string) (*SNMPInfo, bool) {
			return snmpProbe(ctx, ip, community, timeout)
		}
	}
	if o.HTTP == nil {
		tr := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // reads only titles and certificates
			DialContext: (&net.Dialer{Timeout: timeout}).DialContext, TLSHandshakeTimeout: 3 * time.Second, DisableKeepAlives: true}
		o.HTTP = &http.Client{Transport: tr, Timeout: 5 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return http.ErrUseLastResponse
				}
				return nil
			}}
	}
	if o.Lookup == nil {
		o.Lookup = func(ctx context.Context, ip netip.Addr) string {
			c, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			names, err := net.DefaultResolver.LookupAddr(c, ip.String())
			if err != nil || len(names) == 0 {
				return ""
			}
			return strings.TrimSuffix(names[0], ".")
		}
	}
	return o
}

// probe checks one address; ok is false when nothing answered.
func probe(ctx context.Context, o Options, ip netip.Addr) (Host, bool) {
	h := Host{IP: ip.String(), Ports: []int{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Go(func() {
		if o.Ping(ctx, ip) {
			mu.Lock()
			h.Ping = true
			mu.Unlock()
		}
	})
	for _, p := range o.Ports {
		wg.Go(func() {
			c, cancel := context.WithTimeout(ctx, o.Timeout)
			defer cancel()
			conn, err := o.Dial(c, net.JoinHostPort(ip.String(), strconv.Itoa(p)))
			if err != nil {
				return
			}
			conn.Close()
			mu.Lock()
			h.Ports = append(h.Ports, p)
			mu.Unlock()
		})
	}
	if o.Community != "" {
		wg.Go(func() {
			if info, ok := o.SNMP(ctx, ip, o.Community); ok {
				mu.Lock()
				h.SNMP = info
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	slices.Sort(h.Ports)
	if !h.Ping && len(h.Ports) == 0 && h.SNMP == nil {
		return h, false
	}
	h.Name = o.Lookup(ctx, ip)
	if h.Name == "" && h.SNMP != nil {
		h.Name = h.SNMP.Name
	}
	fingerprint(ctx, o, &h)
	Classify(&h)
	return h, true
}

func has(h *Host, port int) bool { return slices.Contains(h.Ports, port) }

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// fingerprint reads the web interface (title, certificate, server) and the Redfish root.
func fingerprint(ctx context.Context, o Options, h *Host) {
	get := func(u string) (*http.Response, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("User-Agent", "PK95-Monitoring-Scan")
		resp, err := o.HTTP.Do(req)
		if err != nil {
			return nil, nil, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return resp, b, nil
	}
	if has(h, PortHTTPS) {
		if resp, b, err := get("https://" + h.IP + "/redfish/v1/"); err == nil && resp.StatusCode == http.StatusOK {
			var root struct {
				Version string `json:"RedfishVersion"`
				Vendor  string `json:"Vendor"`
				Product string `json:"Product"`
			}
			if json.Unmarshal(b, &root) == nil && root.Version != "" {
				h.Redfish = strings.TrimSpace(root.Vendor + " " + root.Product)
				if h.Redfish == "" {
					h.Redfish = "Redfish " + root.Version
				}
			}
		}
	}
	for _, p := range []int{PortPVE, PortPBS, PortUniFi, PortSynology, PortHTTPS, PortHTTP} {
		if !has(h, p) {
			continue
		}
		scheme := "https"
		if p == PortHTTP {
			scheme = "http"
		}
		resp, b, err := get(scheme + "://" + net.JoinHostPort(h.IP, strconv.Itoa(p)) + "/")
		if err != nil {
			continue
		}
		if m := titleRe.FindSubmatch(b); m != nil {
			h.Title = strings.Join(strings.Fields(html.UnescapeString(string(m[1]))), " ")
			if r := []rune(h.Title); len(r) > 80 {
				h.Title = string(r[:80]) + "…"
			}
		}
		h.Server = resp.Header.Get("Server")
		if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
			c := resp.TLS.PeerCertificates[0]
			var parts []string
			if c.Subject.CommonName != "" {
				parts = append(parts, c.Subject.CommonName)
			}
			if org := strings.Join(c.Issuer.Organization, " "); org != "" {
				parts = append(parts, "ausgestellt von "+org)
			} else if c.Issuer.CommonName != "" && c.Issuer.CommonName != c.Subject.CommonName {
				parts = append(parts, "ausgestellt von "+c.Issuer.CommonName)
			}
			h.Cert = strings.Join(parts, ", ")
		}
		return // the first answering interface describes the host
	}
}

// pingHost sends up to two echo requests (unprivileged ICMP first, raw sockets as fallback).
func pingHost(ctx context.Context, ip netip.Addr, timeout time.Duration) bool {
	run := func(privileged bool) (bool, error) {
		p, err := probing.NewPinger(ip.String())
		if err != nil {
			return false, err
		}
		p.SetPrivileged(privileged)
		p.SetLogger(probing.NoopLogger{})
		p.Count, p.Interval, p.Timeout = 2, 300*time.Millisecond, timeout
		if err := p.RunWithContext(ctx); err != nil {
			return false, err
		}
		return p.Statistics().PacketsRecv > 0, nil
	}
	ok, err := run(false)
	if err != nil && errors.Is(err, os.ErrPermission) {
		ok, _ = run(true)
	}
	return ok
}

// OIDs of the system group.
const (
	oidSysDescr    = ".1.3.6.1.2.1.1.1.0"
	oidSysObjectID = ".1.3.6.1.2.1.1.2.0"
	oidSysName     = ".1.3.6.1.2.1.1.5.0"
)

func snmpProbe(ctx context.Context, ip netip.Addr, community string, timeout time.Duration) (*SNMPInfo, bool) {
	s, err := snmp.Dial(ctx, ip.String(), model.SNMPConfig{Version: "2c", Port: 161, Community: community}, timeout)
	if err != nil {
		return nil, false
	}
	defer s.Close()
	pdus, err := s.Get([]string{oidSysDescr, oidSysObjectID, oidSysName})
	if err != nil {
		return nil, false
	}
	return SNMPFromPDUs(pdus)
}

// SNMPFromPDUs reads the system group from an SNMP answer.
func SNMPFromPDUs(pdus []gosnmp.SnmpPDU) (*SNMPInfo, bool) {
	info := &SNMPInfo{}
	val := func(p gosnmp.SnmpPDU) string {
		switch v := p.Value.(type) {
		case []byte:
			return strings.TrimSpace(string(v))
		case string:
			return strings.TrimSpace(v)
		}
		return ""
	}
	for _, p := range pdus {
		switch "." + strings.TrimPrefix(p.Name, ".") {
		case oidSysDescr:
			info.Descr = strings.Join(strings.Fields(val(p)), " ")
		case oidSysObjectID:
			info.ObjectID = strings.TrimPrefix(val(p), ".")
		case oidSysName:
			info.Name = val(p)
		}
	}
	if info.Descr == "" && info.ObjectID == "" {
		return nil, false
	}
	return info, true
}

// enterprise returns the private enterprise number of a sysObjectID (1.3.6.1.4.1.<n>…).
func enterprise(oid string) int {
	rest, ok := strings.CutPrefix(oid, "1.3.6.1.4.1.")
	if !ok {
		return 0
	}
	n, _ := strconv.Atoi(strings.Split(rest, ".")[0])
	return n
}

func containsAny(s string, words ...string) bool {
	s = strings.ToLower(s)
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// Classify decides what a host is and how to monitor it.
func Classify(h *Host) {
	set := func(kind, label, suggest, text string) {
		h.Kind, h.Label, h.Suggest, h.SuggestT = kind, label, suggest, text
	}
	web := strings.ToLower(h.Title + " " + h.Cert + " " + h.Server)
	descr, ent := "", 0
	if h.SNMP != nil {
		descr, ent = strings.ToLower(h.SNMP.Descr), enterprise(h.SNMP.ObjectID)
		h.Detail = h.SNMP.Descr
		if r := []rune(h.Detail); len(r) > 120 {
			h.Detail = string(r[:120]) + "…"
		}
	}
	if h.Detail == "" {
		h.Detail = h.Title
	}
	switch {
	case has(h, PortPVE):
		set(KindProxmox, "Proxmox VE", model.TypeProxmox, "Als Proxmox VE hinzufügen (API-Token mit Leserechten)")
		h.Port = PortPVE
	case has(h, PortPBS):
		set(KindPBS, "Proxmox Backup Server", model.TypePBS, "Als Proxmox Backup Server hinzufügen")
		h.Port = PortPBS
	case has(h, PortVeeam):
		set(KindVeeam, "Veeam Backup & Replication", model.TypeVeeam, "Als Veeam-Server hinzufügen")
		h.Port = PortVeeam
	case h.Redfish != "" || containsAny(web, "idrac", "integrated lights-out", "ilo ", "xclarity"):
		label, all := "Management-Controller", strings.ToLower(h.Redfish+" "+web)
		switch {
		case containsAny(all, "dell", "idrac"):
			label = "Dell iDRAC"
		case containsAny(all, "hpe", "ilo"):
			label = "HPE iLO"
		case containsAny(all, "lenovo", "xclarity", "xcc"):
			label = "Lenovo XCC"
		case containsAny(all, "supermicro"):
			label = "Supermicro BMC"
		}
		set(KindRedfish, label, model.TypeRedfish, "Als Server-Hardware hinzufügen (Redfish, Benutzer mit Leserechten)")
		h.Port = PortHTTPS
	case has(h, PortNUT):
		set(KindNUT, "USV (NUT-Server)", model.TypeNUT, "Als USV (NUT) hinzufügen")
		h.Port = PortNUT
	case h.SNMP != nil:
		switch {
		case ent == 318 || containsAny(descr, "ups", "usv", "smart-ups", "eaton", "riello"):
			set(KindUPS, "USV (SNMP)", model.TypeSNMP, "Als SNMP-Gerät hinzufügen (USV: Akku, Last, Restlaufzeit)")
			h.Profile = snmp.ProfileUPS
		case has(h, PortJetDir) || has(h, PortIPP) || containsAny(descr, "printer", "laserjet", "brother", "kyocera", "ricoh", "lexmark", "xerox", "canon", "epson", "konica", "sharp mx"):
			set(KindPrinter, "Drucker (SNMP)", model.TypeSNMP, "Als SNMP-Gerät hinzufügen (Drucker: Toner, Seiten)")
			h.Profile = snmp.ProfilePrinter
		case ent == 6574 || ent == 24681 || containsAny(descr, "synology", "qnap", "diskstation"):
			set(KindNAS, "NAS (SNMP)", model.TypeSNMP, "Als SNMP-Gerät hinzufügen (NAS: Platten, RAID)")
			h.Profile = snmp.ProfileAuto
		case ent == 41112 || ent == 4413 || containsAny(descr, "unifi", "usw-", "ubiquiti"):
			set(KindUniFi, "UniFi-Gerät (SNMP)", model.TypeSNMP, "Als SNMP-Gerät hinzufügen (UniFi)")
			h.Profile = snmp.ProfileAuto
		case containsAny(descr, "switch", "procurve", "aruba", "cisco ios", "catalyst", "netgear", "zyxel", "tp-link", "edgeswitch", "routeros", "fortigate", "sophos", "pfsense", "opnsense", "lancom"):
			set(KindSwitch, "Netzwerkgerät (SNMP)", model.TypeSNMP, "Als SNMP-Gerät hinzufügen (Ports, Verkehr)")
			h.Profile = snmp.ProfileAuto
		case containsAny(descr, "windows"):
			set(KindWindows, "Windows (SNMP)", "agent", "PK95-Agent installieren – liefert mehr als SNMP")
		case containsAny(descr, "linux"):
			set(KindLinux, "Linux (SNMP)", "agent", "PK95-Agent installieren – liefert mehr als SNMP")
		default:
			set(KindSNMP, "SNMP-Gerät", model.TypeSNMP, "Als SNMP-Gerät hinzufügen")
			h.Profile = snmp.ProfileAuto
		}
	case has(h, PortSynology) || containsAny(web, "synology", "diskstation"):
		set(KindNAS, "Synology NAS", model.TypeSNMP, "SNMP am NAS einschalten (Systemsteuerung → Terminal & SNMP), dann als SNMP-Gerät hinzufügen")
		h.Profile = snmp.ProfileAuto
	case has(h, PortHTTPS) && containsAny(web, "unifi os", "unifi network", "unifi protect", "dream machine", "ubiquiti"):
		set(KindUniFiOS, "UniFi-Konsole", model.TypeUniFi, "Als UniFi-Konsole hinzufügen (lokaler Benutzer mit Leserechten): Geräte, Internet, Kameras")
		h.Port = PortHTTPS
	case has(h, PortUniFi) || containsAny(web, "unifi"):
		set(KindWeb, "UniFi Controller", model.TypeNet, "Mit Netzwerk-Checks überwachen (Ping und Weboberfläche)")
		h.Port = PortUniFi
	case has(h, PortJetDir) || has(h, PortIPP):
		set(KindPrinter, "Drucker", model.TypeSNMP, "SNMP am Drucker einschalten, dann als SNMP-Gerät hinzufügen")
		h.Profile = snmp.ProfilePrinter
	case has(h, PortSMB) || has(h, PortRDP) || has(h, PortWinRM) || has(h, PortMSSQL):
		label := "Windows-Server oder -PC"
		if has(h, PortMSSQL) {
			label = "Windows mit SQL Server"
		}
		set(KindWindows, label, "agent", "PK95-Agent installieren")
	case has(h, PortSSH):
		set(KindLinux, "Linux / Unix (SSH)", "agent", "PK95-Agent installieren")
	case has(h, PortHTTP) || has(h, PortHTTPS):
		label := "Weboberfläche"
		switch {
		case containsAny(web, "fritz!box"):
			label = "FRITZ!Box"
		case h.Title != "":
			label = "Weboberfläche „" + h.Title + "“"
		}
		set(KindWeb, label, model.TypeNet, "Mit Netzwerk-Checks überwachen (Ping und Weboberfläche)")
		h.Port = PortHTTPS
		if !has(h, PortHTTPS) {
			h.Port = PortHTTP
		}
	default:
		set(KindPingOnly, "Antwortet nur auf Ping", model.TypeNet, "Mit einem Ping-Check überwachen")
	}
}
