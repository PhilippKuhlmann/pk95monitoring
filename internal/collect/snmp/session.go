// Package snmp polls devices over SNMP v1, v2c or v3 (gosnmp) and turns the standard MIBs
// (SNMPv2-MIB, IF-MIB, HOST-RESOURCES-MIB, UPS-MIB, Printer-MIB) and a few vendor MIBs
// (APC PowerNet, Synology) into checks. Check keys and metrics follow DESIGN.md.
package snmp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	"pk95monitoring/internal/model"
)

// DefaultTimeout is the timeout of one SNMP request (one retry follows a timeout).
const DefaultTimeout = 5 * time.Second

// getChunk is the number of OIDs sent in one GET request (gosnmp allows 60, but small
// agents answer tooBig long before that).
const getChunk = 20

// Session is one open SNMP connection. Get returns one PDU per requested OID; objects the
// agent does not know come back with type NoSuchObject or NoSuchInstance (never as an
// error). Walk returns every object below root in lexicographic OID order (BulkWalkAll for
// v2c/v3, WalkAll for v1). PDU names may carry a leading dot.
type Session interface {
	Get(oids []string) ([]gosnmp.SnmpPDU, error)
	Walk(root string) ([]gosnmp.SnmpPDU, error)
	Close() error
}

// DialFunc opens a session to host. Dial is the real implementation; tests and the demo
// mode use fakesnmp.Dialer.
type DialFunc func(ctx context.Context, host string, cfg model.SNMPConfig, timeout time.Duration) (Session, error)

// WithDefaults fills the defaults of an SNMP configuration: version 2c, port 161, community
// "public" (not for v3) and profile "auto".
func WithDefaults(cfg model.SNMPConfig) model.SNMPConfig {
	cfg.Version = normVersion(cfg.Version)
	if cfg.Port <= 0 {
		cfg.Port = 161
	}
	if cfg.Community == "" && cfg.Version != "3" {
		cfg.Community = "public"
	}
	cfg.Profile = normProfile(cfg.Profile)
	return cfg
}

func normVersion(v string) string {
	switch strings.TrimPrefix(strings.ToLower(strings.TrimSpace(v)), "v") {
	case "1":
		return "1"
	case "3":
		return "3"
	}
	return "2c"
}

// Dial opens a UDP session with gosnmp. Requests are bound to ctx.
func Dial(ctx context.Context, host string, cfg model.SNMPConfig, timeout time.Duration) (Session, error) {
	g, err := newGoSNMP(ctx, host, cfg, timeout)
	if err != nil {
		return nil, err
	}
	if err := g.Connect(); err != nil {
		return nil, fmt.Errorf("SNMP-Verbindung zu %s: %w", host, err)
	}
	return &client{g: g}, nil
}

// newGoSNMP builds the gosnmp client for a device (without connecting).
func newGoSNMP(ctx context.Context, host string, cfg model.SNMPConfig, timeout time.Duration) (*gosnmp.GoSNMP, error) {
	cfg = WithDefaults(cfg)
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	host = strings.TrimSpace(host)
	port := cfg.Port
	if h, p, err := net.SplitHostPort(host); err == nil {
		if n, err := strconv.Atoi(p); err == nil {
			host, port = h, n
		}
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return nil, errors.New("SNMP: kein Host angegeben")
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("SNMP: ungültiger Port %d", port)
	}
	g := &gosnmp.GoSNMP{
		Context:        ctx,
		Target:         host,
		Port:           uint16(port),
		Transport:      "udp",
		Community:      cfg.Community,
		Version:        gosnmp.Version2c,
		Timeout:        timeout,
		Retries:        1,
		MaxOids:        gosnmp.MaxOids,
		MaxRepetitions: 25,
	}
	switch cfg.Version {
	case "1":
		g.Version = gosnmp.Version1
	case "3":
		flags, usm, err := usmParams(cfg)
		if err != nil {
			return nil, err
		}
		g.Version = gosnmp.Version3
		g.Community = ""
		g.SecurityModel = gosnmp.UserSecurityModel
		g.MsgFlags = flags
		g.SecurityParameters = usm
	}
	return g, nil
}

// usmParams maps the v3 settings of a device to gosnmp's USM parameters. An empty
// security level is derived from the passwords; empty protocols default to SHA and AES.
func usmParams(cfg model.SNMPConfig) (gosnmp.SnmpV3MsgFlags, *gosnmp.UsmSecurityParameters, error) {
	if strings.TrimSpace(cfg.User) == "" {
		return 0, nil, errors.New("SNMPv3: Benutzername fehlt")
	}
	usm := &gosnmp.UsmSecurityParameters{
		UserName:               cfg.User,
		AuthenticationProtocol: gosnmp.NoAuth,
		PrivacyProtocol:        gosnmp.NoPriv,
	}
	level := strings.ToLower(strings.TrimSpace(cfg.SecLevel))
	if level == "" {
		switch {
		case cfg.PrivPass != "":
			level = "authpriv"
		case cfg.AuthPass != "":
			level = "authnopriv"
		default:
			level = "noauthnopriv"
		}
	}
	var flags gosnmp.SnmpV3MsgFlags
	switch level {
	case "noauthnopriv":
		return gosnmp.NoAuthNoPriv, usm, nil
	case "authnopriv":
		flags = gosnmp.AuthNoPriv
	case "authpriv":
		flags = gosnmp.AuthPriv
	default:
		return 0, nil, fmt.Errorf("SNMPv3: unbekannte Sicherheitsstufe %q", cfg.SecLevel)
	}

	auth, ok := authProtos[protoKey(cfg.AuthProto)]
	if !ok {
		return 0, nil, fmt.Errorf("SNMPv3: unbekanntes Authentifizierungsprotokoll %q", cfg.AuthProto)
	}
	if cfg.AuthPass == "" {
		return 0, nil, errors.New("SNMPv3: Authentifizierungspasswort fehlt")
	}
	usm.AuthenticationProtocol = auth
	usm.AuthenticationPassphrase = cfg.AuthPass
	if flags == gosnmp.AuthNoPriv {
		return flags, usm, nil
	}

	priv, ok := privProtos[protoKey(cfg.PrivProto)]
	if !ok {
		return 0, nil, fmt.Errorf("SNMPv3: unbekanntes Verschlüsselungsprotokoll %q", cfg.PrivProto)
	}
	if cfg.PrivPass == "" {
		return 0, nil, errors.New("SNMPv3: Verschlüsselungspasswort fehlt")
	}
	usm.PrivacyProtocol = priv
	usm.PrivacyPassphrase = cfg.PrivPass
	return flags, usm, nil
}

// protoKey normalizes protocol names: "sha-256" → "SHA256".
func protoKey(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	return strings.NewReplacer("-", "", "_", "", " ", "").Replace(s)
}

var authProtos = map[string]gosnmp.SnmpV3AuthProtocol{
	"":       gosnmp.SHA,
	"MD5":    gosnmp.MD5,
	"SHA":    gosnmp.SHA,
	"SHA1":   gosnmp.SHA,
	"SHA224": gosnmp.SHA224,
	"SHA256": gosnmp.SHA256,
	"SHA384": gosnmp.SHA384,
	"SHA512": gosnmp.SHA512,
}

// privProtos: AES192/AES256 are the Blumenthal variants (net-snmp and most devices),
// AES192C/AES256C the Reeder variants used by Cisco.
var privProtos = map[string]gosnmp.SnmpV3PrivProtocol{
	"":        gosnmp.AES,
	"DES":     gosnmp.DES,
	"AES":     gosnmp.AES,
	"AES128":  gosnmp.AES,
	"AES192":  gosnmp.AES192,
	"AES256":  gosnmp.AES256,
	"AES192C": gosnmp.AES192C,
	"AES256C": gosnmp.AES256C,
}

// client is the gosnmp implementation of Session.
type client struct{ g *gosnmp.GoSNMP }

func (c *client) Get(oids []string) ([]gosnmp.SnmpPDU, error) {
	out := make([]gosnmp.SnmpPDU, 0, len(oids))
	for len(oids) > 0 {
		n := min(len(oids), getChunk)
		pdus, err := c.get(oids[:n])
		if err != nil {
			return nil, err
		}
		out = append(out, pdus...)
		oids = oids[n:]
	}
	return out, nil
}

// get sends one GET. SNMPv1 agents (and some broken v2c agents) fail the whole request
// with an error status when one OID is unknown; then every OID is asked for alone and the
// failing ones are returned as NoSuchObject.
func (c *client) get(oids []string) ([]gosnmp.SnmpPDU, error) {
	pkt, err := c.g.Get(oids)
	if err != nil {
		return nil, err
	}
	if pkt.Error == gosnmp.NoError {
		return pkt.Variables, nil
	}
	if len(oids) == 1 {
		return []gosnmp.SnmpPDU{{Name: oids[0], Type: gosnmp.NoSuchObject}}, nil
	}
	out := make([]gosnmp.SnmpPDU, 0, len(oids))
	for _, oid := range oids {
		pdus, err := c.get([]string{oid})
		if err != nil {
			return nil, err
		}
		out = append(out, pdus...)
	}
	return out, nil
}

func (c *client) Walk(root string) ([]gosnmp.SnmpPDU, error) {
	if c.g.Version == gosnmp.Version1 {
		return c.g.WalkAll(root)
	}
	return c.g.BulkWalkAll(root)
}

func (c *client) Close() error { return c.g.Close() }
