// Command pk95agent monitors a Linux or Windows host for PK95 Monitoring.
//
//	pk95agent install --server URL --key KEY [--fingerprint SHA256] [--insecure] [--device ID [--node NODE]]
//	pk95agent uninstall
//	pk95agent run        (service mode; also usable in the foreground)
//	pk95agent once       (print one report as JSON)
//	pk95agent version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"pk95monitoring/internal/agent"
)

// version is set by the release build (-ldflags "-X main.version=…").
var version = "dev"

func main() {
	agent.Version = version
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "install":
		err = install(args)
	case "uninstall":
		err = uninstall(args)
	case "run":
		err = runService(args)
	case "once":
		err = once(args)
	case "version", "--version", "-v":
		fmt.Printf("pk95agent %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
	case "help", "--help", "-h":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Fehler:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `PK95 Monitoring Agent %s

Aufruf: pk95agent <Befehl> [Optionen]

  install    registrieren und als Dienst einrichten (root/Administrator)
             --server URL --key SCHLÜSSEL [--fingerprint SHA256] [--insecure]
             [--device GERÄTE-ID [--node KNOTEN]]  an ein vorhandenes Gerät anhängen
  uninstall  Dienst und Konfiguration entfernen
  run        Agent ausführen (Dienstmodus, auch im Vordergrund nutzbar)
  once       einen Bericht erfassen und als JSON ausgeben
  version    Version anzeigen

Alle Befehle: --config PFAD (Standard: %s)
`, version, agent.DefaultConfigPath())
}

func configFlag(fs *flag.FlagSet) *string {
	return fs.String("config", agent.DefaultConfigPath(), "Pfad der Konfigurationsdatei")
}

func install(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	server := fs.String("server", "", "Server-URL, z. B. https://monitoring.example:8443")
	key := fs.String("key", "", "Registrierungsschlüssel aus der Weboberfläche")
	fingerprint := fs.String("fingerprint", "", "SHA-256-Fingerprint des Serverzertifikats (selbstsigniert)")
	insecure := fs.Bool("insecure", false, "Zertifikat nicht prüfen (nur zum Testen)")
	device := fs.String("device", "", "an dieses vorhandene Gerät anhängen (Geräte-ID, z. B. Proxmox-Cluster)")
	node := fs.String("node", "", "Teil des Geräts, zu dem der Host gehört (z. B. Proxmox-Knoten)")
	cfgPath := configFlag(fs)
	_ = fs.Parse(args)

	srv, err := checkServerURL(*server)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*key) == "" {
		return errors.New("--key fehlt (Registrierungsschlüssel aus der Weboberfläche, Seite „Agenten“)")
	}
	fp, err := agent.NormalizeFingerprint(*fingerprint)
	if err != nil {
		return err
	}
	dev, nd, err := checkDevice(*device, *node)
	if err != nil {
		return err
	}
	if !isAdmin() {
		return errors.New(adminHint)
	}

	fmt.Printf("Registrierung bei %s …\n", srv)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	reg, err := agent.RegisterDevice(ctx, srv, strings.TrimSpace(*key), fp, *insecure, dev, nd)
	if err != nil {
		return fmt.Errorf("Registrierung fehlgeschlagen: %w", err)
	}

	target := installPath()
	s, err := newService(*cfgPath, target)
	if err != nil {
		return err
	}
	_, statusErr := s.Status()
	installed := statusErr == nil
	if installed {
		fmt.Println("Vorhandener Dienst wird gestoppt …")
		_ = s.Stop()
		waitStopped(s)
	}

	// Write the config only after the old service stopped: it would otherwise save its own
	// config (old token) when the server changes its settings. Re-install keeps the service
	// settings the server pushed before.
	cfg, _ := agent.Load(*cfgPath)
	cfg.Server, cfg.Token, cfg.DeviceID = srv, reg.Token, reg.DeviceID
	cfg.Fingerprint, cfg.Insecure = fp, *insecure
	if reg.Interval > 0 {
		cfg.Interval = reg.Interval
	}
	if err := agent.Save(*cfgPath, cfg); err != nil {
		return fmt.Errorf("Konfiguration speichern (%s): %w", *cfgPath, err)
	}

	if err := installBinary(target); err != nil {
		return fmt.Errorf("Programm nach %s kopieren: %w", target, err)
	}

	if installed {
		_ = s.Uninstall()
	}
	if err := s.Install(); err != nil {
		return fmt.Errorf("Dienst einrichten: %w", err)
	}
	if err := agent.ConfigureRecovery(agent.ServiceName); err != nil {
		fmt.Fprintln(os.Stderr, "Hinweis: Wiederherstellungsoptionen des Dienstes nicht gesetzt:", err)
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("Dienst starten: %w", err)
	}
	fmt.Printf("\nPK95 Monitoring Agent %s ist installiert und läuft.\n", version)
	fmt.Printf("  Gerät:         %s\n", reg.DeviceID)
	if dev != "" {
		at := dev
		if nd != "" {
			at += " (Knoten " + nd + ")"
		}
		fmt.Printf("  Gehört zu:     %s\n", at)
	}
	fmt.Printf("  Programm:      %s\n", target)
	fmt.Printf("  Konfiguration: %s\n", *cfgPath)
	fmt.Println("Der Host erscheint in wenigen Sekunden in der Weboberfläche.")
	return nil
}

// checkDevice validates --device and --node (node only together with a device).
func checkDevice(device, node string) (string, string, error) {
	device, node = strings.TrimSpace(device), strings.TrimSpace(node)
	if node != "" && device == "" {
		return "", "", errors.New("--node nur zusammen mit --device angeben")
	}
	if len(device) > 64 || strings.ContainsFunc(device, unsafeRune) {
		return "", "", errors.New("--device: ungültige Geräte-ID")
	}
	if len([]rune(node)) > 100 || strings.ContainsFunc(node, unsafeRune) {
		return "", "", errors.New("--node: ungültiger Name")
	}
	return device, node, nil
}

func unsafeRune(r rune) bool { return r < 0x20 || r == 0x7f || r == ' ' }

func checkServerURL(raw string) (string, error) {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(s)
	if s == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", errors.New("--server muss eine URL mit https:// sein, z. B. https://monitoring.example:8443")
	}
	if u.Scheme == "http" {
		fmt.Fprintln(os.Stderr, "Hinweis: Die Verbindung zum Server ist unverschlüsselt (http).")
	}
	return s, nil
}

func uninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	cfgPath := configFlag(fs)
	_ = fs.Parse(args)
	if !isAdmin() {
		return errors.New(adminHint)
	}
	s, err := newService(*cfgPath, installPath())
	if err != nil {
		return err
	}
	if _, err := s.Status(); err == nil {
		fmt.Println("Dienst wird gestoppt und entfernt …")
		_ = s.Stop()
		waitStopped(s)
		if err := s.Uninstall(); err != nil {
			return fmt.Errorf("Dienst entfernen: %w", err)
		}
	} else {
		fmt.Println("Kein Dienst eingerichtet.")
	}
	removed := removeConfig(*cfgPath)
	if removed {
		fmt.Println("Konfiguration und Zwischenspeicher entfernt.")
	}
	removeInstalledBinary()
	fmt.Println("PK95 Monitoring Agent ist deinstalliert. Das Gerät kann in der Weboberfläche gelöscht werden.")
	return nil
}

// once prints one report. It works without registration (then without configured services).
func once(args []string) error {
	fs := flag.NewFlagSet("once", flag.ExitOnError)
	cfgPath := configFlag(fs)
	_ = fs.Parse(args)
	cfg, err := agent.Load(*cfgPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "Hinweis:", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	c := agent.NewCollector()
	c.WaitSlow = true
	b, err := json.MarshalIndent(c.Report(ctx, cfg), "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
