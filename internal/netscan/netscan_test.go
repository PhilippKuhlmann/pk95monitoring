package netscan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"pk95monitoring/internal/model"
)

func TestExpand(t *testing.T) {
	a, norm, err := Expand([]string{" 10.0.0.5/24 ", "10.0.1.10-10.0.1.12", "10.0.0.7", ""})
	if err != nil || len(a) != 254+3 || strings.Join(norm, ",") != "10.0.0.0/24,10.0.1.10-10.0.1.12,10.0.0.7" {
		t.Fatalf("expand %d %v %v", len(a), norm, err)
	}
	if a[0].String() != "10.0.0.1" || a[253].String() != "10.0.0.254" {
		t.Errorf("network and broadcast: %v … %v", a[0], a[253])
	}
	for _, bad := range []string{"10.0.0.0/16", "fe80::1", "10.0.0.9-10.0.0.1", "kein netz"} {
		if _, _, err := Expand([]string{bad}); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	many := []string{}
	for i := range 17 {
		many = append(many, fmt.Sprintf("10.0.%d.0/24", i))
	}
	if _, _, err := Expand(many); err == nil || !strings.Contains(err.Error(), "4096") {
		t.Errorf("too many addresses: %v", err)
	}
}

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		h       Host
		kind    string
		suggest string
	}{
		{Host{Ports: []int{PortSSH, PortHTTPS, PortPVE}}, KindProxmox, model.TypeProxmox},
		{Host{Ports: []int{PortPBS}}, KindPBS, model.TypePBS},
		{Host{Ports: []int{PortSMB, PortRDP, PortVeeam}}, KindVeeam, model.TypeVeeam},
		{Host{Ports: []int{PortHTTPS}, Redfish: "Dell Inc. Integrated Dell Remote Access Controller"}, KindRedfish, model.TypeRedfish},
		{Host{Ports: []int{PortHTTP, PortJetDir}, SNMP: &SNMPInfo{Descr: "Brother NC-8300h, Firmware Ver.1.10"}}, KindPrinter, model.TypeSNMP},
		{Host{SNMP: &SNMPInfo{Descr: "Network Management Card", ObjectID: "1.3.6.1.4.1.318.1.3.27"}}, KindUPS, model.TypeSNMP},
		{Host{SNMP: &SNMPInfo{Descr: "Linux DS920", ObjectID: "1.3.6.1.4.1.6574.1"}}, KindNAS, model.TypeSNMP},
		{Host{SNMP: &SNMPInfo{Descr: "HP ProCurve J9772A Switch 2530-48G-PoE+"}}, KindSwitch, model.TypeSNMP},
		{Host{SNMP: &SNMPInfo{Descr: "Hardware: Intel64 - Software: Windows Version 6.3"}}, KindWindows, "agent"},
		{Host{Ports: []int{PortSMB, PortRDP, PortMSSQL}}, KindWindows, "agent"},
		{Host{Ports: []int{PortSSH}}, KindLinux, "agent"},
		{Host{Ports: []int{PortIPP}}, KindPrinter, model.TypeSNMP},
		{Host{Ports: []int{PortHTTP, PortHTTPS}, Title: "FRITZ!Box 7590"}, KindWeb, model.TypeNet},
		{Host{Ping: true, Ports: []int{}}, KindPingOnly, model.TypeNet},
		{Host{Ports: []int{PortSSH, PortHTTPS, PortSynology}}, KindNAS, model.TypeSNMP},
		{Host{Ports: []int{PortSSH, PortUniFi}, Title: "UniFi Network"}, KindWeb, model.TypeNet},
		{Host{Ports: []int{PortSSH, PortHTTPS}, Title: "UniFi OS"}, KindUniFiOS, model.TypeUniFi},
	} {
		h := tc.h
		Classify(&h)
		if h.Kind != tc.kind || h.Suggest != tc.suggest || h.Label == "" || h.SuggestT == "" {
			t.Errorf("%+v: %s/%s %q", tc.h, h.Kind, h.Suggest, h.Label)
		}
	}
	h := Host{Ports: []int{PortSMB, PortMSSQL}}
	Classify(&h)
	if h.Label != "Windows mit SQL Server" {
		t.Errorf("label %q", h.Label)
	}
	h = Host{Ports: []int{PortHTTP}, Title: "FRITZ!Box 7590"}
	Classify(&h)
	if h.Label != "FRITZ!Box" || h.Port != PortHTTP {
		t.Errorf("web %+v", h)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestScan(t *testing.T) {
	addrs, _, _ := Expand([]string{"10.9.0.0/29"}) // .1 – .6
	open := map[string]bool{"10.9.0.1:8006": true, "10.9.0.1:443": true, "10.9.0.2:445": true, "10.9.0.2:3389": true, "10.9.0.5:80": true}
	page := map[string]string{"https://10.9.0.1:8006/": "<html><title>pve1 - Proxmox Virtual Environment</title>", "http://10.9.0.5:80/": "<TITLE> FRITZ!Box &amp; Co </TITLE>"}
	var pmu sync.Mutex
	progress := 0
	hosts := Scan(context.Background(), Options{Addrs: addrs, Community: "public",
		Progress: func(done, total int) { pmu.Lock(); progress = max(progress, done); pmu.Unlock() },
		Ping:     func(ctx context.Context, ip netip.Addr) bool { return ip.String() == "10.9.0.3" },
		Dial: func(ctx context.Context, addr string) (net.Conn, error) {
			if open[addr] {
				a, b := net.Pipe()
				b.Close()
				return a, nil
			}
			return nil, errors.New("refused")
		},
		SNMP: func(ctx context.Context, ip netip.Addr, community string) (*SNMPInfo, bool) {
			if ip.String() == "10.9.0.4" && community == "public" {
				return &SNMPInfo{Descr: "Smart-UPS 1500", ObjectID: "1.3.6.1.4.1.318.1.3.5", Name: "usv-keller"}, true
			}
			return nil, false
		},
		HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			body, ok := page[r.URL.String()]
			if !ok {
				return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Server": {"pve-api-daemon/3.0"}}}, nil
		})},
		Lookup: func(ctx context.Context, ip netip.Addr) string {
			if ip.String() == "10.9.0.2" {
				return "srv-buchhaltung.kunde.local"
			}
			return ""
		},
	})
	if progress != 6 || len(hosts) != 5 {
		t.Fatalf("progress %d, hosts %+v", progress, hosts)
	}
	byIP := map[string]Host{}
	for _, h := range hosts {
		byIP[h.IP] = h
	}
	if h := byIP["10.9.0.1"]; h.Kind != KindProxmox || h.Title != "pve1 - Proxmox Virtual Environment" || h.Server != "pve-api-daemon/3.0" || h.Port != PortPVE {
		t.Errorf("proxmox %+v", h)
	}
	if h := byIP["10.9.0.2"]; h.Kind != KindWindows || h.Name != "srv-buchhaltung.kunde.local" {
		t.Errorf("windows %+v", h)
	}
	if h := byIP["10.9.0.3"]; h.Kind != KindPingOnly || !h.Ping {
		t.Errorf("ping %+v", h)
	}
	if h := byIP["10.9.0.4"]; h.Kind != KindUPS || h.Name != "usv-keller" || h.Profile != "ups" {
		t.Errorf("ups %+v", h)
	}
	if h := byIP["10.9.0.5"]; h.Title != "FRITZ!Box & Co" || h.Label != "FRITZ!Box" {
		t.Errorf("web %+v", h)
	}
	if hosts[0].IP != "10.9.0.1" || hosts[4].IP != "10.9.0.5" {
		t.Errorf("order %v %v", hosts[0].IP, hosts[4].IP)
	}
}
