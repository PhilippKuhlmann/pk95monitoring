package agentdist

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"text/template"
)

//go:embed install.sh.tmpl install.ps1.tmpl
var templateFS embed.FS

var templates = template.Must(template.New("").Funcs(template.FuncMap{
	"shq": shQuote,
	"psq": psQuote,
}).ParseFS(templateFS, "*.tmpl"))

// Params fill the install scripts and one-liners.
type Params struct {
	ServerURL   string // e.g. https://monitor.example:8443 (no trailing slash needed)
	Fingerprint string // SHA-256 of a self-signed server certificate; empty with a CA certificate
	// PubKeyPin is the base64 SHA-256 of the server certificate's SubjectPublicKeyInfo. When
	// set, curl pins the bootstrap downloads with --pinnedpubkey (see PinFromCert).
	PubKeyPin string
	Version   string // agent version the server distributes
	// DeviceID (optional) attaches the installed agent to that device ("pk95agent install
	// --device"); Node optionally names the part of it, e.g. the Proxmox node.
	DeviceID string
	Node     string
}

// clean keeps only characters that are safe inside quoted shell, cmd.exe and PowerShell
// strings. Server URL, key, fingerprint and hashes never need anything else; dropping the rest
// makes injection through a misconfigured URL impossible.
func (p Params) clean() Params {
	return Params{
		ServerURL:   strings.TrimRight(keep(p.ServerURL, "-._~:/?#[]@+,;="), "/"),
		Fingerprint: normalizeFingerprint(p.Fingerprint),
		PubKeyPin:   cleanPin(p.PubKeyPin),
		Version:     keep(p.Version, "-._+"),
		DeviceID:    clipRunes(keep(p.DeviceID, "-_"), 64),
		Node:        clipRunes(keep(p.Node, "-._"), 100),
	}
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// cleanPin returns the pin when it is a base64 SHA-256 value, "" otherwise.
func cleanPin(pin string) string {
	pin = strings.TrimPrefix(keep(pin, "+/="), "sha256//")
	if b, err := base64.StdEncoding.DecodeString(pin); err != nil || len(b) != sha256.Size {
		return ""
	}
	return pin
}

// PinFromCert returns the curl --pinnedpubkey value of a certificate (without "sha256//"):
// the base64 SHA-256 of its SubjectPublicKeyInfo.
func PinFromCert(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func keep(s, extra string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(extra, r) {
			return r
		}
		return -1
	}, strings.TrimSpace(s))
}

// normalizeFingerprint returns lower-case hex without separators ("" stays "").
func normalizeFingerprint(fp string) string {
	return strings.ToLower(keep(strings.NewReplacer(":", "", " ", "", "-", "").Replace(fp), ""))
}

// shQuote quotes s for POSIX shells.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// psQuote quotes s as a PowerShell single-quoted string.
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// InstallScript renders install.sh ("sh") or install.ps1 ("ps1").
func InstallScript(kind string, p Params) ([]byte, error) {
	var name string
	switch kind {
	case "sh":
		name = "install.sh.tmpl"
	case "ps1":
		name = "install.ps1.tmpl"
	default:
		return nil, fmt.Errorf("unbekanntes Installationsskript %q", kind)
	}
	p = p.clean()
	if p.ServerURL == "" {
		return nil, errors.New("Server-URL fehlt")
	}
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, name, p); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if kind == "ps1" {
		// Windows PowerShell reads scripts in the ANSI code page: CRLF and ASCII only.
		out = bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n"))
	}
	return out, nil
}

// Commands returns the one-liners for the web UI. An empty string means the binary for that
// system is not available on the server.
func Commands(p Params, key string, bins []Binary) (linux, windows string) {
	p = p.clean()
	key = keep(key, "-._~+=/")
	sums := map[string]string{}
	for _, b := range bins {
		if b.Available && b.SHA256 != "" {
			sums[b.OS+"/"+b.Arch] = strings.ToLower(keep(b.SHA256, ""))
		}
	}

	if sums["linux/amd64"] != "" || sums["linux/arm64"] != "" {
		var sb strings.Builder
		sb.WriteString("curl -fsSL")
		if p.Fingerprint != "" {
			sb.WriteString(" -k")
		}
		if p.PubKeyPin != "" {
			// -k skips the name check of the self-signed certificate; the pin still binds the
			// download to this server's key.
			fmt.Fprintf(&sb, " --pinnedpubkey %s", shQuote("sha256//"+p.PubKeyPin))
		}
		// $(command -v sudo) is empty when sudo is missing (e.g. Proxmox VE as root).
		fmt.Fprintf(&sb, " %s | $(command -v sudo) bash -s -- --key %s", shQuote(p.ServerURL+"/agent/install.sh"), shQuote(key))
		if p.Fingerprint != "" {
			fmt.Fprintf(&sb, " --fingerprint %s", p.Fingerprint)
		}
		for _, arch := range []string{"amd64", "arm64"} {
			if s := sums["linux/"+arch]; s != "" {
				fmt.Fprintf(&sb, " --sha256-%s %s", arch, s)
			}
		}
		if p.DeviceID != "" {
			fmt.Fprintf(&sb, " --device %s", shQuote(p.DeviceID))
			if p.Node != "" {
				fmt.Fprintf(&sb, " --node %s", shQuote(p.Node))
			}
		}
		linux = sb.String()
	}

	if s := sums["windows/amd64"]; s != "" {
		// No "$", "%", '"' or backticks inside: the line works pasted into cmd.exe and PowerShell.
		var ps strings.Builder
		ps.WriteString("[Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12; ")
		if p.Fingerprint != "" {
			// Accept exactly the certificate with this SHA-256 fingerprint. "Get-Variable args"
			// reads the callback arguments without a dollar sign (sender, certificate, …).
			fmt.Fprintf(&ps, "[Net.ServicePointManager]::ServerCertificateValidationCallback={ "+
				"([BitConverter]::ToString([Security.Cryptography.SHA256]::Create().ComputeHash("+
				"(Get-Variable args -ValueOnly)[1].GetRawCertData())) -replace '-') -eq %s }; ", psQuote(p.Fingerprint))
		}
		fmt.Fprintf(&ps, "& ([scriptblock]::Create((New-Object Net.WebClient).DownloadString(%s))) -Key %s",
			psQuote(p.ServerURL+"/agent/install.ps1"), psQuote(key))
		if p.Fingerprint != "" {
			fmt.Fprintf(&ps, " -Fingerprint %s", psQuote(p.Fingerprint))
		}
		fmt.Fprintf(&ps, " -Sha256 %s", psQuote(s))
		if p.DeviceID != "" {
			fmt.Fprintf(&ps, " -Device %s", psQuote(p.DeviceID))
			if p.Node != "" {
				fmt.Fprintf(&ps, " -Node %s", psQuote(p.Node))
			}
		}
		windows = `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "` + ps.String() + `"`
	}
	return linux, windows
}
