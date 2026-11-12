package proxmox

import (
	"context"
	"errors"
	"strings"

	"pk95monitoring/internal/model"
)

// TokenHint explains how to create a read-only API token on a PVE node (shown in the UI).
const TokenHint = `Auf einem Proxmox-Knoten als root in der Shell ausführen:

pveum user add pk95mon@pve --comment "PK95 Monitoring"
pveum acl modify / --users pk95mon@pve --roles PVEAuditor
pveum user token add pk95mon@pve monitoring --privsep 0

Token-ID: pk95mon@pve!monitoring – das Secret ("value") wird nur einmal angezeigt.
PVEAuditor genügt für Cluster, Knoten, Gäste, Speicher, ZFS, SMART und Sicherungen.

Optional für die Update-Prüfung (Proxmox verlangt dafür das Recht Sys.Modify auf /nodes;
es erlaubt auch Änderungen am Knoten, z. B. Paketquellen aktualisieren):

pveum role add PK95MonUpdates --privs Sys.Modify
pveum acl modify /nodes --users pk95mon@pve --roles PK95MonUpdates

Ohne dieses Recht entfallen die Update-Checks; der Knoten zeigt dann einen Hinweis.`

// TestResult is the answer of a successful connection test.
type TestResult struct {
	Version string   `json:"version"`
	Nodes   []string `json:"nodes"`
	Guests  int      `json:"guests"`
}

// Test checks connection, certificate and token. Errors are German and meant for the UI.
func Test(ctx context.Context, host string, cfg model.ProxmoxConfig) (TestResult, error) {
	host = strings.TrimSpace(host)
	switch {
	case host == "":
		return TestResult{}, errors.New("Host fehlt")
	case strings.TrimSpace(cfg.TokenID) == "" || strings.TrimSpace(cfg.Secret) == "":
		return TestResult{}, errors.New("Token-ID und Secret angeben")
	case !strings.Contains(cfg.TokenID, "!"):
		return TestResult{}, errors.New("Token-ID im Format benutzer@realm!tokenname angeben (z. B. pk95mon@pve!monitoring)")
	}
	cl := newClient(host, cfg)
	defer cl.close()
	ver, err := cl.version(ctx)
	if err != nil {
		return TestResult{}, friendly(err)
	}
	res, err := cl.resources(ctx)
	if err != nil {
		return TestResult{}, friendly(err)
	}
	inv := newInventory(res)
	if len(inv.nodes) == 0 {
		return TestResult{}, errNoNodes
	}
	out := TestResult{Version: ver.Version, Guests: len(inv.guests), Nodes: []string{}}
	for _, n := range inv.nodes {
		out.Nodes = append(out.Nodes, n.Node)
	}
	return out, nil
}
