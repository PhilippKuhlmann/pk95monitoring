package agent

import (
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"pk95monitoring/internal/model"
)

const aptSample = `NOTE: This is only a simulation!
      apt-get needs root privileges for real execution.
Reading package lists...
Building dependency tree...
Reading state information...
Calculating upgrade...
The following packages will be upgraded:
  libc6 openssl curl
3 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.
Inst libc6 [2.36-9+deb12u3] (2.36-9+deb12u4 Debian:12.5/stable [amd64])
Inst openssl [3.0.11-1~deb12u2] (3.0.13-1~deb12u1 Debian-Security:12/stable-security [amd64])
Inst curl [7.88.1-10+deb12u4] (7.88.1-10+deb12u5 Debian:12.5/stable, Debian-Security:12/stable-security [amd64])
Conf libc6 (2.36-9+deb12u4 Debian:12.5/stable [amd64])
Conf openssl (3.0.13-1~deb12u1 Debian-Security:12/stable-security [amd64])
`

const aptUbuntu = `Inst libssl3 [3.0.2-0ubuntu1.14] (3.0.2-0ubuntu1.15 Ubuntu:22.04/jammy-updates, Ubuntu:22.04/jammy-security [amd64])
Inst tzdata [2024a-0ubuntu0.22.04] (2024a-0ubuntu0.22.04.1 Ubuntu:22.04/jammy-updates [all])
`

func TestParseAptUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want updateCount
	}{
		{"debian", aptSample, updateCount{Pending: 3, Security: 2}},
		{"ubuntu", aptUbuntu, updateCount{Pending: 2, Security: 1}},
		{"none", "Reading package lists...\n0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n", updateCount{}},
		{"empty", "", updateCount{}},
	} {
		if got := parseAptUpgrade(tc.out); got.Pending != tc.want.Pending || got.Security != tc.want.Security {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

const dnfSample = `
kernel.x86_64                     5.14.0-362.24.1.el9_3         baseos
openssl-libs.x86_64               1:3.0.7-25.el9_3              baseos
a-very-long-package-name-that-wraps.noarch
                                  1.2.3-4.el9                   appstream
Obsoleting Packages
grub2-tools.x86_64                1:2.06-70.el9                 baseos
    grub2-tools.x86_64            1:2.06-61.el9                 @baseos
`

func TestParseDnf(t *testing.T) {
	if n := parseDnfCheckUpdate(dnfSample); n != 3 {
		t.Errorf("check-update: %d, want 3", n)
	}
	if n := parseDnfCheckUpdate(""); n != 0 {
		t.Errorf("empty: %d", n)
	}
	dnf4 := `RHSA-2024:1234 Important/Sec. openssl-libs-1:3.0.7-25.el9_3.x86_64
RHSA-2024:1234 Important/Sec. openssl-1:3.0.7-25.el9_3.x86_64
RHSA-2024:2222 Moderate/Sec.  kernel-5.14.0-362.24.1.el9_3.x86_64
`
	if n := parseDnfSecurity(dnf4); n != 3 {
		t.Errorf("dnf4 security: %d, want 3", n)
	}
	dnf5 := `Name                Type     Severity  Package                              Issued
FEDORA-2024-abc     security Important openssl-1:3.1.4-3.fc40.x86_64         2024-05-01 00:00:00
FEDORA-2024-abc     security Important openssl-libs-1:3.1.4-3.fc40.x86_64    2024-05-01 00:00:00
`
	if n := parseDnfSecurity(dnf5); n != 2 {
		t.Errorf("dnf5 security: %d, want 2", n)
	}
}

func TestParseSystemctl(t *testing.T) {
	failed := "nginx.service  loaded failed failed A high performance web server\n● smartd.service loaded failed failed Self Monitoring\n\n"
	if got := parseFailedUnits(failed); !slices.Equal(got, []string{"nginx.service", "smartd.service"}) {
		t.Errorf("failed units %v", got)
	}
	if got := parseFailedUnits(""); len(got) != 0 {
		t.Errorf("no failed units: %v", got)
	}
	states := parseIsActive([]string{"sshd", "nginx", "missing"}, "active\nfailed\n")
	if states["sshd"] != "active" || states["nginx"] != "failed" || states["missing"] != "unknown" {
		t.Errorf("is-active %v", states)
	}
	if unitStateText("inactive") != "gestoppt" || unitStateText("weird") != "weird" {
		t.Error("unitStateText")
	}
}

func TestParsePSJSON(t *testing.T) {
	var c updateCount
	if err := parsePSJSON("WARNUNG: irgendwas\r\n{\"pending\":5,\"security\":2,\"titles\":[\"KB1\"],\"last_install\":1760000000,\"failed\":1}\r\n", &c); err != nil ||
		c.Pending != 5 || c.Security != 2 || len(c.Titles) != 1 || c.LastInstall != 1760000000 || c.Failed != 1 {
		t.Fatalf("got %+v, %v", c, err)
	}
	var e eventlogResult
	if err := parsePSJSON(`{"errors":12,"critical":1,"last":"Kernel-Power (41): Das System wurde neu gestartet"}`, &e); err != nil || e.Errors != 12 || e.Critical != 1 {
		t.Fatalf("eventlog %+v, %v", e, err)
	}
	if err := parsePSJSON("Zugriff verweigert", &c); err == nil {
		t.Error("garbage accepted")
	}
}

func TestEncodePS(t *testing.T) {
	script := "Write-Output 'ä'"
	raw, err := base64.StdEncoding.DecodeString(encodePS(script))
	if err != nil || len(raw)%2 != 0 {
		t.Fatal(err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	if got := string(utf16.Decode(u)); got != script {
		t.Errorf("round trip %q", got)
	}
}

func TestBuildUpdatesCheck(t *testing.T) {
	ch := buildUpdatesCheck(updateCount{Pending: 5, Security: 2}, nil, false)
	if ch.Key != "updates" || ch.State != model.StateOK || ch.Message != "5 Updates, davon 2 Sicherheitsupdates" || len(ch.Metrics) != 2 ||
		ch.Metrics[0].Name != "updates.pending" || ch.Metrics[1].Name != "updates.security" || ch.Metrics[1].Value != 2 {
		t.Errorf("check %+v", ch)
	}
	ch = buildUpdatesCheck(updateCount{}, nil, true)
	if ch.State != model.StateWarn || !strings.HasPrefix(ch.Message, "Neustart erforderlich") {
		t.Errorf("reboot: %+v", ch)
	}
	ch = buildUpdatesCheck(updateCount{}, errors.New("lock"), false)
	if ch.State != model.StateUnknown || len(ch.Metrics) != 0 {
		t.Errorf("error: %+v", ch)
	}
	if updatesMessage(updateCount{}) != "System ist aktuell" || updatesMessage(updateCount{Pending: 1}) != "1 Update verfügbar" {
		t.Error("updatesMessage")
	}
}

func TestUpdateHistory(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.Local)
	ch := model.Check{Message: "2 Updates verfügbar"}
	addUpdateHistory(&ch, updateCount{Pending: 7, Titles: []string{"a", "b", "c", "d", "e", "f", "g"}, Failed: 2,
		LastInstall: now.Add(-40*24*time.Hour - time.Hour).Unix()}, now)
	if len(ch.Metrics) != 2 || ch.Metrics[0].Name != "updates.age" || ch.Metrics[0].Value != 40 || ch.Metrics[1].Value != 2 {
		t.Fatalf("metrics %+v", ch.Metrics)
	}
	if ch.Message != "2 Updates verfügbar · letzte Installation vor 40 Tagen · 2 fehlgeschlagen" {
		t.Fatalf("message %q", ch.Message)
	}
	if ch.Facts["Ausstehend"] != "a · b · c · d · e · und 2 weitere" || ch.Facts["Fehlgeschlagen (30 Tage)"] != "2" || ch.Facts["Letzte Installation"] == "" {
		t.Fatalf("facts %+v", ch.Facts)
	}
	// Linux: no history, no extra metrics
	ch = model.Check{}
	addUpdateHistory(&ch, updateCount{Pending: 1}, now)
	if len(ch.Metrics) != 0 || ch.Facts != nil {
		t.Fatalf("linux %+v", ch)
	}
}

func TestEventlogCheck(t *testing.T) {
	ch := eventlogCheck(eventlogResult{Errors: 7}, nil)
	if ch.State != model.StateOK || ch.Metrics[0].Name != "eventlog.errors" || ch.Metrics[0].Value != 7 {
		t.Errorf("ok: %+v", ch)
	}
	ch = eventlogCheck(eventlogResult{Errors: 9, Critical: 1, Last: "Kernel-Power (41): neu gestartet"}, nil)
	if ch.State != model.StateWarn || !strings.Contains(ch.Message, "Kernel-Power") {
		t.Errorf("critical: %+v", ch)
	}
	if ch := eventlogCheck(eventlogResult{}, errors.New("x")); ch.State != model.StateUnknown {
		t.Errorf("error: %+v", ch)
	}
}

func TestFormat(t *testing.T) {
	for _, tc := range []struct {
		got, want string
	}{
		{humanBytes(512), "512 B"},
		{humanBytes(1536), "1,5 KB"},
		{humanBytes(50 * 1 << 30), "50,0 GB"},
		{humanBytes(500 * 1 << 30), "500 GB"},
		{humanDuration(90061), "1 Tag 1 Std."},
		{humanDuration(3 * 86400), "3 Tage 0 Std."},
		{humanDuration(3700), "1 Std. 1 Min."},
		{humanDuration(59), "0 Min."},
		{pct(4.56), "4,6 %"},
		{pct(91.2), "91 %"},
		{truncate("abcdef", 4), "abc…"},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
}
