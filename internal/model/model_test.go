package model

import "testing"

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		p, s string
		want bool
	}{
		{"snmp:if:*", "snmp:if:Gi1/0/1", true},
		{"snmp:if:*", "snmp:ifx", false},
		{"disk:/boot*", "disk:/boot/efi", true},
		{"disk:/boot*", "disk:/", false},
		{"pve:*:pve1", "pve:updates:pve1", true},
		{"pve:*:pve1", "pve:updates:pve2", false},
		{"*", "anything", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxbyy", false},
		{"exact", "exact", true},
		{"exact", "exactly", false},
	}
	for _, c := range cases {
		if got := GlobMatch(c.p, c.s); got != c.want {
			t.Errorf("GlobMatch(%q, %q) = %v, want %v", c.p, c.s, got, c.want)
		}
	}
}

func TestCheckMode(t *testing.T) {
	d := Device{Rules: []CheckRule{
		{Match: "snmp:if:*", Mode: ModeQuiet},
		{Match: "snmp:if:Gi1/0/48", Mode: ModeAlert},
		{Match: "snmp:if:Vlan*", Mode: ModeOff},
		{Match: "*", Mode: "bogus"},
	}}
	cases := map[string]string{
		"snmp:if:Gi1/0/1":  ModeQuiet, // pattern
		"snmp:if:Gi1/0/48": ModeAlert, // exact wins over pattern
		"snmp:if:Vlan10":   ModeOff,   // longer pattern wins
		"snmp:cpu":         ModeAlert, // unknown mode -> alert
		ReachKey:           ModeAlert, // never ruled
	}
	for key, want := range cases {
		if got := d.CheckMode(key); got != want {
			t.Errorf("CheckMode(%q) = %q, want %q", key, got, want)
		}
	}
	if got := (Device{}).CheckMode("cpu"); got != ModeAlert {
		t.Errorf("default = %q", got)
	}
}
