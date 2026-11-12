package snmp

import (
	"context"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"pk95monitoring/internal/model"
)

func TestUSMParams(t *testing.T) {
	tests := []struct {
		name    string
		cfg     model.SNMPConfig
		flags   gosnmp.SnmpV3MsgFlags
		auth    gosnmp.SnmpV3AuthProtocol
		priv    gosnmp.SnmpV3PrivProtocol
		wantErr bool
	}{
		{"noAuthNoPriv", model.SNMPConfig{User: "mon", SecLevel: "noAuthNoPriv"}, gosnmp.NoAuthNoPriv, gosnmp.NoAuth, gosnmp.NoPriv, false},
		{"derived noAuth", model.SNMPConfig{User: "mon"}, gosnmp.NoAuthNoPriv, gosnmp.NoAuth, gosnmp.NoPriv, false},
		{"authNoPriv MD5", model.SNMPConfig{User: "mon", SecLevel: "authNoPriv", AuthProto: "MD5", AuthPass: "secret12"}, gosnmp.AuthNoPriv, gosnmp.MD5, gosnmp.NoPriv, false},
		{"authNoPriv default SHA", model.SNMPConfig{User: "mon", SecLevel: "authNoPriv", AuthPass: "secret12"}, gosnmp.AuthNoPriv, gosnmp.SHA, gosnmp.NoPriv, false},
		{"derived authPriv", model.SNMPConfig{User: "mon", AuthPass: "a1234567", PrivPass: "p1234567"}, gosnmp.AuthPriv, gosnmp.SHA, gosnmp.AES, false},
		{"SHA224 DES", model.SNMPConfig{User: "mon", SecLevel: "authPriv", AuthProto: "SHA224", AuthPass: "a1234567", PrivProto: "DES", PrivPass: "p1234567"}, gosnmp.AuthPriv, gosnmp.SHA224, gosnmp.DES, false},
		{"SHA256 AES192", model.SNMPConfig{User: "mon", SecLevel: "authpriv", AuthProto: "sha-256", AuthPass: "a1234567", PrivProto: "AES192", PrivPass: "p1234567"}, gosnmp.AuthPriv, gosnmp.SHA256, gosnmp.AES192, false},
		{"SHA384 AES256", model.SNMPConfig{User: "mon", SecLevel: "authPriv", AuthProto: "SHA384", AuthPass: "a1234567", PrivProto: "AES256", PrivPass: "p1234567"}, gosnmp.AuthPriv, gosnmp.SHA384, gosnmp.AES256, false},
		{"SHA512 AES256C", model.SNMPConfig{User: "mon", SecLevel: "authPriv", AuthProto: "SHA512", AuthPass: "a1234567", PrivProto: "aes256c", PrivPass: "p1234567"}, gosnmp.AuthPriv, gosnmp.SHA512, gosnmp.AES256C, false},
		{"SHA1 AES128", model.SNMPConfig{User: "mon", SecLevel: "authPriv", AuthProto: "SHA1", AuthPass: "a1234567", PrivProto: "AES128", PrivPass: "p1234567"}, gosnmp.AuthPriv, gosnmp.SHA, gosnmp.AES, false},
		{"no user", model.SNMPConfig{SecLevel: "noAuthNoPriv"}, 0, 0, 0, true},
		{"bad level", model.SNMPConfig{User: "mon", SecLevel: "everything"}, 0, 0, 0, true},
		{"bad auth proto", model.SNMPConfig{User: "mon", SecLevel: "authNoPriv", AuthProto: "SHA3", AuthPass: "a1234567"}, 0, 0, 0, true},
		{"missing auth pass", model.SNMPConfig{User: "mon", SecLevel: "authNoPriv"}, 0, 0, 0, true},
		{"bad priv proto", model.SNMPConfig{User: "mon", SecLevel: "authPriv", AuthPass: "a1234567", PrivProto: "3DES", PrivPass: "p1234567"}, 0, 0, 0, true},
		{"missing priv pass", model.SNMPConfig{User: "mon", SecLevel: "authPriv", AuthPass: "a1234567"}, 0, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags, usm, err := usmParams(tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if flags != tt.flags || usm.AuthenticationProtocol != tt.auth || usm.PrivacyProtocol != tt.priv {
				t.Errorf("got flags %v auth %v priv %v", flags, usm.AuthenticationProtocol, usm.PrivacyProtocol)
			}
			if usm.UserName != tt.cfg.User || usm.AuthenticationPassphrase != authPass(tt.flags, tt.cfg) ||
				usm.PrivacyPassphrase != privPass(tt.flags, tt.cfg) {
				t.Errorf("credentials not mapped: %+v", usm)
			}
		})
	}
}

func authPass(f gosnmp.SnmpV3MsgFlags, cfg model.SNMPConfig) string {
	if f == gosnmp.NoAuthNoPriv {
		return ""
	}
	return cfg.AuthPass
}

func privPass(f gosnmp.SnmpV3MsgFlags, cfg model.SNMPConfig) string {
	if f != gosnmp.AuthPriv {
		return ""
	}
	return cfg.PrivPass
}

func TestNewGoSNMP(t *testing.T) {
	ctx := context.Background()
	g, err := newGoSNMP(ctx, "10.0.0.5", model.SNMPConfig{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if g.Version != gosnmp.Version2c || g.Port != 161 || g.Community != "public" || g.Retries != 1 ||
		g.Timeout != DefaultTimeout || g.Target != "10.0.0.5" {
		t.Errorf("defaults: %+v", g)
	}

	g, err = newGoSNMP(ctx, "[fd00::1]:1161", model.SNMPConfig{Version: "1", Community: "geheim"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if g.Version != gosnmp.Version1 || g.Port != 1161 || g.Target != "fd00::1" || g.Community != "geheim" || g.Timeout != time.Second {
		t.Errorf("v1 with port: %+v", g)
	}

	g, err = newGoSNMP(ctx, "switch.local", model.SNMPConfig{Version: "3", Port: 10161, User: "mon",
		AuthProto: "SHA256", AuthPass: "a1234567", PrivProto: "AES", PrivPass: "p1234567"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	usm, _ := g.SecurityParameters.(*gosnmp.UsmSecurityParameters)
	if g.Version != gosnmp.Version3 || g.Community != "" || g.SecurityModel != gosnmp.UserSecurityModel ||
		g.MsgFlags != gosnmp.AuthPriv || g.Port != 10161 || usm == nil || usm.AuthenticationProtocol != gosnmp.SHA256 {
		t.Errorf("v3: %+v", g)
	}

	for _, bad := range []struct {
		host string
		cfg  model.SNMPConfig
	}{
		{"", model.SNMPConfig{}},
		{"host", model.SNMPConfig{Port: 70000}},
		{"host", model.SNMPConfig{Version: "3"}},
	} {
		if _, err := newGoSNMP(ctx, bad.host, bad.cfg, 0); err == nil {
			t.Errorf("%q %+v: expected an error", bad.host, bad.cfg)
		}
	}
}

func TestWithDefaults(t *testing.T) {
	c := WithDefaults(model.SNMPConfig{})
	if c.Version != "2c" || c.Port != 161 || c.Community != "public" || c.Profile != ProfileAuto {
		t.Errorf("defaults: %+v", c)
	}
	c = WithDefaults(model.SNMPConfig{Version: "v3", Profile: "UPS"})
	if c.Version != "3" || c.Community != "" || c.Profile != ProfileUPS {
		t.Errorf("v3: %+v", c)
	}
	if c := WithDefaults(model.SNMPConfig{Version: "1", Community: "x"}); c.Version != "1" || c.Community != "x" {
		t.Errorf("v1: %+v", c)
	}
}

func TestCompareOID(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.3.6.1.2", "1.3.6.1.10", -1},
		{".1.3.6.1.10", "1.3.6.1.2", 1},
		{"1.3.6", "1.3.6", 0},
		{"1.3.6", "1.3.6.1", -1},
		{"1.3.6.1.2.1.2.2.1.10.9", "1.3.6.1.2.1.2.2.1.10.10", -1},
	}
	for _, tt := range tests {
		if got := CompareOID(tt.a, tt.b); got != tt.want {
			t.Errorf("CompareOID(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestRate(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name    string
		prev    ctr
		cur     ctr
		maxRate float64
		want    float64
		ok      bool
	}{
		{"increase", ctr{1000, 64}, ctr{11000, 64}, 0, 1000, true},
		{"32-bit wrap", ctr{4294967000, 32}, ctr{1000, 32}, 0, 129.6, true},
		{"32-bit wrap at line rate ok", ctr{4294967000, 32}, ctr{1000, 32}, 125e6, 129.6, true},
		{"32-bit implausible wrap", ctr{100, 32}, ctr{50, 32}, 125e6, 0, false},
		{"32-bit reset without speed", ctr{1_000_000_000, 32}, ctr{5, 32}, 0, 0, false},
		{"64-bit reset", ctr{1e9, 64}, ctr{5, 64}, 0, 0, false},
		{"width changed", ctr{1000, 32}, ctr{2000, 64}, 0, 0, false},
		{"faster than line", ctr{0, 64}, ctr{2e9, 64}, 125e6, 0, false},
		{"missing", ctr{1, 64}, ctr{}, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &devState{ctr: map[string]sample{}}
			if _, ok := st.rate("k", tt.prev, t0, tt.maxRate); ok {
				t.Fatal("first sample must not give a rate")
			}
			got, ok := st.rate("k", tt.cur, t0.Add(10*time.Second), tt.maxRate)
			if ok != tt.ok || (ok && (got < tt.want-0.01 || got > tt.want+0.01)) {
				t.Errorf("got %v %v, want %v %v", got, ok, tt.want, tt.ok)
			}
		})
	}
	st := &devState{ctr: map[string]sample{}}
	st.rate("k", ctr{1, 64}, t0, 0)
	if _, ok := st.rate("k", ctr{100, 64}, t0, 0); ok {
		t.Error("same timestamp must not give a rate")
	}

	// the clock was set back: start over instead of waiting until it passes the old sample
	back := t0.Add(-time.Hour)
	if _, ok := st.rate("k", ctr{200, 64}, back, 0); ok {
		t.Error("clock set back must not give a rate")
	}
	if r, ok := st.rate("k", ctr{1200, 64}, back.Add(10*time.Second), 0); !ok || r != 100 {
		t.Errorf("rate after the clock was set back = %v %v, want 100", r, ok)
	}
}

func TestFormatting(t *testing.T) {
	tests := []struct{ got, want string }{
		{fmtSpeed(1e9), "1 Gbit/s"},
		{fmtSpeed(2_500_000_000), "2,5 Gbit/s"},
		{fmtSpeed(100e6), "100 Mbit/s"},
		{fmtBytes(512), "512 B"},
		{fmtBytes(3.5 * (1 << 30)), "3,5 GiB"},
		{fmtUptime(90061), "1 Tag 1 Std."},
		{fmtUptime(3 * 86400), "3 Tage 0 Std."},
		{fmtUptime(3720), "1 Std. 2 Min."},
		{fmtPct(41.6), "42 %"},
		{fmtNum(23.5, 2), "23,5"},
		{storageName(`C:\ Label:System  Serial Number 1a2b`), "C:"},
		{storageName("/volume1"), "/volume1"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("got %q, want %q", tt.got, tt.want)
		}
	}
}

func TestErrorBits(t *testing.T) {
	got := errorBits([]byte{0x40, 0x04}) // noPaper (bit 1), inputTrayEmpty (bit 13)
	if len(got) != 2 || got[0] != 1 || got[1] != 13 {
		t.Errorf("errorBits = %v", got)
	}
}

func TestCleanStringAndValues(t *testing.T) {
	if s := cleanString([]byte("Cyan Toner\x00\x00")); s != "Cyan Toner" {
		t.Errorf("NULs: %q", s)
	}
	if s := cleanString([]byte{'B', 0xfc, 'r', 'o'}); s != "Büro" {
		t.Errorf("Latin-1: %q", s)
	}
	if f, ok := toFloat(gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: []byte("23.5 ")}); !ok || f != 23.5 {
		t.Errorf("numeric string: %v %v", f, ok)
	}
	if _, ok := toFloat(gosnmp.SnmpPDU{Type: gosnmp.NoSuchObject}); ok {
		t.Error("noSuchObject must not be a number")
	}
	if v := int32u(-1); v != 4294967295 {
		t.Errorf("int32u = %v", v)
	}
	if !validOID(".1.3.6.1.4.1.9.0") || validOID("1.3.x") || validOID("") {
		t.Error("validOID")
	}
	if enterprise(".1.3.6.1.4.1.318.1.3.27") != 318 || enterprise("1.3.6.1.2.1") != 0 {
		t.Error("enterprise")
	}
}
