package netcheck

import (
	"net"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"pk95monitoring/internal/model"
)

// zone is the content of the test DNS server: name → answers (any type).
type zone map[string][]dnsmessage.ResourceBody

// startDNS runs a minimal UDP DNS server on localhost and returns its address.
// Names missing from the zone get NXDOMAIN; known names without records of the
// asked type get an empty NOERROR answer.
func startDNS(t *testing.T, z zone) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if resp, err := answer(buf[:n], z); err == nil {
				pc.WriteTo(resp, from)
			}
		}
	}()
	return pc.LocalAddr().String()
}

func answer(query []byte, z zone) ([]byte, error) {
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil {
		return nil, err
	}
	q, err := p.Question()
	if err != nil {
		return nil, err
	}
	name := strings.ToLower(q.Name.String())
	records, known := z[name]
	rh := dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true,
		RecursionDesired: h.RecursionDesired, RecursionAvailable: true}
	if !known {
		rh.RCode = dnsmessage.RCodeNameError
	}
	b := dnsmessage.NewBuilder(nil, rh)
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(q); err != nil {
		return nil, err
	}
	if err := b.StartAnswers(); err != nil {
		return nil, err
	}
	for _, body := range records {
		// A CNAME is the answer to every query type of an alias (as in real DNS).
		if typeOf(body) != q.Type && typeOf(body) != dnsmessage.TypeCNAME {
			continue
		}
		hdr := dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 60}
		switch r := body.(type) {
		case *dnsmessage.AResource:
			err = b.AResource(hdr, *r)
		case *dnsmessage.AAAAResource:
			err = b.AAAAResource(hdr, *r)
		case *dnsmessage.MXResource:
			err = b.MXResource(hdr, *r)
		case *dnsmessage.TXTResource:
			err = b.TXTResource(hdr, *r)
		case *dnsmessage.NSResource:
			err = b.NSResource(hdr, *r)
		case *dnsmessage.CNAMEResource:
			err = b.CNAMEResource(hdr, *r)
		}
		if err != nil {
			return nil, err
		}
	}
	return b.Finish()
}

func typeOf(body dnsmessage.ResourceBody) dnsmessage.Type {
	switch body.(type) {
	case *dnsmessage.AResource:
		return dnsmessage.TypeA
	case *dnsmessage.AAAAResource:
		return dnsmessage.TypeAAAA
	case *dnsmessage.MXResource:
		return dnsmessage.TypeMX
	case *dnsmessage.TXTResource:
		return dnsmessage.TypeTXT
	case *dnsmessage.NSResource:
		return dnsmessage.TypeNS
	case *dnsmessage.CNAMEResource:
		return dnsmessage.TypeCNAME
	}
	return 0
}

func TestDNS(t *testing.T) {
	server := startDNS(t, zone{
		"example.test.": {
			&dnsmessage.AResource{A: [4]byte{192, 0, 2, 1}},
			&dnsmessage.AResource{A: [4]byte{192, 0, 2, 2}},
			&dnsmessage.AAAAResource{AAAA: [16]byte{0x20, 0x01, 0x0d, 0xb8, 15: 1}},
			&dnsmessage.MXResource{Pref: 10, MX: dnsmessage.MustNewName("mail.example.test.")},
			&dnsmessage.TXTResource{TXT: []string{"v=spf1 -all"}},
			&dnsmessage.NSResource{NS: dnsmessage.MustNewName("ns1.example.test.")},
		},
		"empty.example.test.": {},
		"www.example.test.":   {&dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName("example.test.")}},
	})

	tests := []struct {
		name  string
		c     model.NetCheck
		state string
		msg   string
	}{
		{"A", model.NetCheck{Target: "example.test"}, model.StateOK, "A: 192.0.2.1, 192.0.2.2"},
		{"A expect", model.NetCheck{Target: "example.test", Expect: "192.0.2.2"}, model.StateOK, "A: "},
		{"A mismatch", model.NetCheck{Target: "example.test", Expect: "192.0.2.9"}, model.StateCrit, "Erwartet 192.0.2.9, erhalten 192.0.2.1, 192.0.2.2"},
		{"AAAA", model.NetCheck{Target: "example.test", RecordType: "AAAA", Expect: "2001:db8::1"}, model.StateOK, "AAAA: 2001:db8::1"},
		{"MX", model.NetCheck{Target: "example.test.", RecordType: "mx", Expect: "MAIL.example.test."}, model.StateOK, "MX: mail.example.test"},
		{"TXT", model.NetCheck{Target: "example.test", RecordType: "TXT", Expect: "v=spf1 -all"}, model.StateOK, "TXT: v=spf1 -all"},
		{"NS", model.NetCheck{Target: "example.test", RecordType: "NS"}, model.StateOK, "NS: ns1.example.test"},
		{"TXT mismatch", model.NetCheck{Target: "example.test", RecordType: "TXT", Expect: "v=spf1 +all"}, model.StateCrit, "Erwartet v=spf1 +all, erhalten v=spf1 -all"},
		{"NXDOMAIN", model.NetCheck{Target: "missing.example.test"}, model.StateCrit, "Kein A-Eintrag für missing.example.test (Name unbekannt"},
		{"CNAME", model.NetCheck{Target: "www.example.test", RecordType: "CNAME", Expect: "example.test"}, model.StateOK, "CNAME: example.test"},
		{"CNAME mismatch", model.NetCheck{Target: "www.example.test", RecordType: "CNAME", Expect: "other.test"}, model.StateCrit, "Erwartet other.test, erhalten example.test"},
		{"no CNAME", model.NetCheck{Target: "example.test", RecordType: "CNAME"}, model.StateCrit, "Kein CNAME-Eintrag für example.test"},
		{"no records", model.NetCheck{Target: "empty.example.test", RecordType: "MX"}, model.StateCrit, "Kein MX-Eintrag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.c.Type, tt.c.Server, tt.c.Timeout = model.NetDNS, server, 3
			c := runCheck(tt.c)
			expectState(t, c, tt.state, tt.msg)
			_, hasTime := metric(c, "dns.time")
			if tt.state == model.StateOK && !hasTime {
				t.Fatal("dns.time missing")
			}
		})
	}
}

func TestDNSServerDown(t *testing.T) {
	// A UDP socket that never answers.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	c := runCheck(model.NetCheck{Type: model.NetDNS, Target: "example.test", Server: pc.LocalAddr().String(), Timeout: 1})
	expectState(t, c, model.StateCrit, "Zeitüberschreitung")
}
