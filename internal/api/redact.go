package api

import (
	"bytes"
	"encoding/json"
	"fmt"

	"pk95monitoring/internal/core"
	"pk95monitoring/internal/model"
	"pk95monitoring/internal/notify"
)

// Secrets never leave the server: they are sent as "" plus a has_… flag, and an empty secret
// on save keeps the stored one (core.SaveDevice, core.SaveSettings).

type proxmoxOut struct {
	model.ProxmoxConfig
	Secret    string `json:"secret"`
	HasSecret bool   `json:"has_secret"`
}

type snmpOut struct {
	model.SNMPConfig
	Community    string `json:"community"`
	AuthPass     string `json:"auth_pass"`
	PrivPass     string `json:"priv_pass"`
	HasCommunity bool   `json:"has_community"`
	HasAuthPass  bool   `json:"has_auth_pass"`
	HasPrivPass  bool   `json:"has_priv_pass"`
}

type nutOut struct {
	model.NUTConfig
	Password    string `json:"password"`
	HasPassword bool   `json:"has_password"`
}

// deviceOut is a device without secrets (the outer fields shadow the embedded ones in JSON).
type veeamOut struct {
	model.VeeamConfig
	Password    string `json:"password"`
	HasPassword bool   `json:"has_password"`
}

type deviceOut struct {
	model.Device
	Proxmox *proxmoxOut `json:"proxmox,omitempty"`
	SNMP    *snmpOut    `json:"snmp,omitempty"`
	NUT     *nutOut     `json:"nut,omitempty"`
	Veeam   *veeamOut   `json:"veeam,omitempty"`
}

func redactDevice(d model.Device) deviceOut {
	out := deviceOut{Device: d}
	if d.Checks == nil {
		out.Checks = []model.NetCheck{}
	}
	if d.Thresholds == nil {
		out.Thresholds = []model.Threshold{}
	}
	if d.Rules == nil {
		out.Rules = []model.CheckRule{}
	}
	if p := d.Proxmox; p != nil {
		c := *p
		c.Secret = ""
		out.Proxmox = &proxmoxOut{ProxmoxConfig: c, HasSecret: p.Secret != ""}
	}
	if sn := d.SNMP; sn != nil {
		c := *sn
		c.Community, c.AuthPass, c.PrivPass = "", "", ""
		out.SNMP = &snmpOut{SNMPConfig: c, HasCommunity: sn.Community != "", HasAuthPass: sn.AuthPass != "", HasPrivPass: sn.PrivPass != ""}
	}
	if n := d.NUT; n != nil {
		c := *n
		c.Password = ""
		out.NUT = &nutOut{NUTConfig: c, HasPassword: n.Password != ""}
	}
	if v := d.Veeam; v != nil {
		c := *v
		c.Password = ""
		out.Veeam = &veeamOut{VeeamConfig: c, HasPassword: v.Password != ""}
	}
	out.Device.Proxmox, out.Device.SNMP, out.Device.NUT, out.Device.Veeam = nil, nil, nil, nil
	return out
}

type notifyOut struct {
	notify.Settings
	SMTPPass         string `json:"smtp_password"`
	HookToken        string `json:"hook_token"`
	TelegramToken    string `json:"telegram_token"`
	HasSMTPPassword  bool   `json:"has_smtp_password"`
	HasHookToken     bool   `json:"has_hook_token"`
	HasTelegramToken bool   `json:"has_telegram_token"`
}

func redactNotify(n notify.Settings) notifyOut {
	out := notifyOut{Settings: n, HasSMTPPassword: n.SMTPPass != "", HasHookToken: n.HookToken != "", HasTelegramToken: n.TelegramToken != ""}
	out.Settings.SMTPPass, out.Settings.HookToken, out.Settings.TelegramToken = "", "", ""
	return out
}

type centralOut struct {
	Enabled  bool   `json:"enabled"`
	URL      string `json:"url"`
	HasToken bool   `json:"has_token"`
	Interval int    `json:"interval"`
}

// settingsOut is GET /api/settings (and the answer of PUT).
type settingsOut struct {
	General    core.GeneralSettings `json:"general"`
	Notify     notifyOut            `json:"notify"`
	Thresholds []model.Threshold    `json:"thresholds"`
	Defaults   []model.Threshold    `json:"defaults"`
	Central    centralOut           `json:"central"`
	Agent      core.AgentSettings   `json:"agent"`
	Report     core.ReportSettings  `json:"report"`
}

func redactSettings(s core.Settings) settingsOut {
	th := s.Thresholds
	if th == nil {
		th = []model.Threshold{}
	}
	return settingsOut{
		General:    s.General,
		Notify:     redactNotify(s.Notify),
		Thresholds: th,
		Defaults:   model.DefaultThresholds(),
		Central:    centralOut{Enabled: s.Central.Enabled, URL: s.Central.URL, HasToken: s.Central.Token != "", Interval: s.Central.Interval},
		Agent:      s.Agent,
		Report:     s.Report,
	}
}

// mergeSettings applies a PUT /api/settings body to the current settings. Every section is
// optional: a missing (or null) section stays unchanged, keys missing inside a section keep
// their value, and "thresholds": [] resets the global thresholds to the defaults. Empty
// secrets are passed on as "", which core treats as "keep the stored one".
func mergeSettings(cur core.Settings, body []byte) (core.Settings, error) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(body, &in); err != nil {
		return cur, err
	}
	present := func(k string) (json.RawMessage, bool) {
		raw, ok := in[k]
		return raw, ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	}
	if raw, ok := present("general"); ok {
		if err := json.Unmarshal(raw, &cur.General); err != nil {
			return cur, fmt.Errorf("general: %w", err)
		}
	}
	if raw, ok := present("notify"); ok {
		if err := json.Unmarshal(raw, &cur.Notify); err != nil {
			return cur, fmt.Errorf("notify: %w", err)
		}
	}
	if raw, ok := present("thresholds"); ok {
		th := []model.Threshold{}
		if err := json.Unmarshal(raw, &th); err != nil {
			return cur, fmt.Errorf("thresholds: %w", err)
		}
		cur.Thresholds = th
	}
	if raw, ok := present("central"); ok {
		if err := json.Unmarshal(raw, &cur.Central); err != nil {
			return cur, fmt.Errorf("central: %w", err)
		}
	}
	if raw, ok := present("agent"); ok {
		if err := json.Unmarshal(raw, &cur.Agent); err != nil {
			return cur, fmt.Errorf("agent: %w", err)
		}
	}
	if raw, ok := present("report"); ok {
		if err := json.Unmarshal(raw, &cur.Report); err != nil {
			return cur, fmt.Errorf("report: %w", err)
		}
	}
	return cur, nil
}
