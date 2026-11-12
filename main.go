// PK95 Monitoring: monitoring server for Proxmox VE, Linux/Windows hosts (pk95agent),
// SNMP devices and network checks.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"pk95monitoring/internal/app"
)

// version is set by the release build (-ldflags "-X main.version=…").
var version = "dev"

func main() {
	cfg := app.Config{
		Version:      version,
		Listen:       env("PK95MON_LISTEN", ":8443"),
		HTTPRedirect: env("PK95MON_HTTP_REDIRECT", ""),
		DataDir:      env("PK95MON_DATA_DIR", "/data"),
		AgentDir:     env("PK95MON_AGENT_DIR", "/opt/pk95monitoring/agents"),
		PublicURL:    strings.TrimSpace(os.Getenv("PK95MON_PUBLIC_URL")),
		Instance:     strings.TrimSpace(os.Getenv("PK95MON_INSTANCE")),
		Demo:         flag("PK95MON_DEMO"),
		NoTLS:        flag("PK95MON_NO_TLS"),
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func flag(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
