// Package netcheck runs the network checks of a device: ping, HTTP(S), TLS certificate,
// TCP port and DNS. Every check returns a model.Check; failures never surface as Go errors
// but as crit (the target misbehaves) or unknown (the check could not run) with a German
// message.
package netcheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"pk95monitoring/internal/model"
)

// Group is the UI section of all network checks.
const Group = "Netzwerk-Checks"

const (
	defaultTimeout = 10 * time.Second
	maxTimeout     = 120 // seconds
	userAgent      = "PK95-Monitoring/1.0"
)

// recordTypes are the DNS record types a dns check can query.
var recordTypes = []string{"A", "AAAA", "CNAME", "MX", "TXT", "NS"}

// Run executes one network check against a device with the given host. It never fails:
// errors become a crit or unknown state with a German message.
func Run(ctx context.Context, c model.NetCheck, host string) model.Check {
	res := model.Check{Key: c.Key(), Name: checkName(c, host), Group: Group, State: model.StateUnknown}
	if errs := Validate(c, host); len(errs) > 0 {
		res.Message = "Konfiguration ungültig: " + firstError(errs)
		return res
	}
	timeout := checkTimeout(c)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch c.Type {
	case model.NetPing:
		runPing(ctx, &res, pingTarget(c, host), timeout)
	case model.NetHTTP:
		runHTTP(ctx, &res, c, timeout)
	case model.NetTCP:
		addr, _ := endpoint(c.Target, host, "")
		runTCP(ctx, &res, addr, timeout)
	case model.NetTLS:
		addr, _ := endpoint(c.Target, host, "443")
		runTLS(ctx, &res, addr, timeout)
	case model.NetDNS:
		runDNS(ctx, &res, c, timeout)
	}
	if res.State == model.StateCrit && errors.Is(ctx.Err(), context.Canceled) {
		// The caller gave up (shutdown, device deleted): not a fault of the target. ctx.Err
		// (not context.Cause) so a custom cancel cause of the caller is recognized too.
		res.State, res.Message = model.StateUnknown, "Prüfung abgebrochen"
	}
	return res
}

// DefaultName is the label of a check without its own name, e.g. "Ping 10.0.0.1".
func DefaultName(c model.NetCheck, host string) string {
	switch c.Type {
	case model.NetPing:
		return "Ping " + pingTarget(c, host)
	case model.NetHTTP:
		return "HTTP " + redactURL(strings.TrimSpace(c.Target))
	case model.NetTCP:
		return "TCP " + endpointName(c.Target, host, "")
	case model.NetTLS:
		return "Zertifikat " + endpointName(c.Target, host, "443")
	case model.NetDNS:
		return fmt.Sprintf("DNS %s (%s)", strings.TrimSpace(c.Target), recordType(c))
	}
	return "Netzwerk-Check"
}

// Validate checks a network check configuration. The result maps JSON field names to
// German error messages; it is empty when the check is valid.
func Validate(c model.NetCheck, host string) map[string]string {
	errs := map[string]string{}
	target := strings.TrimSpace(c.Target)
	switch c.Type {
	case model.NetPing:
		t := pingTarget(c, host)
		if t == "" {
			errs["target"] = "Ziel fehlt (und das Gerät hat keinen Host)"
		} else if !validHost(t) {
			errs["target"] = "Ungültiger Hostname oder IP-Adresse"
		}
	case model.NetHTTP:
		u, err := url.Parse(target)
		if target == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs["target"] = "URL mit http:// oder https:// erforderlich"
		}
		m := strings.ToUpper(strings.TrimSpace(c.Method))
		if m != "" && m != "GET" && m != "HEAD" {
			errs["method"] = "Methode muss GET oder HEAD sein"
		}
		if c.ExpectStatus != 0 && (c.ExpectStatus < 100 || c.ExpectStatus > 599) {
			errs["expect_status"] = "Statuscode muss zwischen 100 und 599 liegen"
		}
		if c.Keyword != "" && m == "HEAD" {
			errs["keyword"] = "Suchbegriff ist nur mit GET möglich"
		}
	case model.NetTCP:
		if _, err := endpoint(c.Target, host, ""); err != nil {
			errs["target"] = err.Error()
		}
	case model.NetTLS:
		if _, err := endpoint(c.Target, host, "443"); err != nil {
			errs["target"] = err.Error()
		}
	case model.NetDNS:
		if target == "" {
			errs["target"] = "Name fehlt"
		} else if !validHost(strings.TrimSuffix(target, ".")) {
			errs["target"] = "Ungültiger DNS-Name"
		}
		if c.RecordType != "" && !slices.Contains(recordTypes, strings.ToUpper(strings.TrimSpace(c.RecordType))) {
			errs["record_type"] = "Typ muss " + strings.Join(recordTypes, ", ") + " sein"
		}
		if strings.TrimSpace(c.Server) != "" {
			if _, err := endpoint(c.Server, "", "53"); err != nil {
				errs["server"] = "Ungültiger DNS-Server (Host oder Host:Port)"
			}
		}
	case "":
		errs["type"] = "Typ fehlt"
	default:
		errs["type"] = "Unbekannter Typ (ping, http, tcp, tls, dns)"
	}
	if c.Timeout < 0 || c.Timeout > maxTimeout {
		errs["timeout"] = fmt.Sprintf("Zeitlimit muss zwischen 1 und %d Sekunden liegen", maxTimeout)
	}
	return errs
}

// redactURL hides a password in the URL: the name is shown in the UI and in alerts.
func redactURL(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		return u.Redacted()
	}
	return raw
}

// endpointName is host:port for a valid target, else the target as entered.
func endpointName(target, host, defPort string) string {
	if addr, err := endpoint(target, host, defPort); err == nil {
		return addr
	}
	return strings.TrimSpace(target)
}

func checkName(c model.NetCheck, host string) string {
	if n := strings.TrimSpace(c.Name); n != "" {
		return n
	}
	return DefaultName(c, host)
}

func checkTimeout(c model.NetCheck) time.Duration {
	if c.Timeout > 0 {
		return time.Duration(c.Timeout) * time.Second
	}
	return defaultTimeout
}

// firstError returns the message of the alphabetically first field (deterministic).
func firstError(errs map[string]string) string {
	keys := make([]string, 0, len(errs))
	for k := range errs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return errs[keys[0]]
}

func pingTarget(c model.NetCheck, host string) string {
	if t := strings.TrimSpace(c.Target); t != "" {
		return t
	}
	return strings.TrimSpace(host)
}

func recordType(c model.NetCheck) string {
	if t := strings.ToUpper(strings.TrimSpace(c.RecordType)); t != "" {
		return t
	}
	return "A"
}

// endpoint turns a target into host:port. The host defaults to the device host (so a
// target ":22" or "22" checks the device itself); the port defaults to defPort. Without
// a default port the target must name one.
func endpoint(target, host, defPort string) (string, error) {
	target = strings.TrimSpace(target)
	h, p := target, ""
	if sh, sp, err := net.SplitHostPort(target); err == nil {
		h, p = sh, sp
	} else if _, err := strconv.Atoi(target); err == nil && target != "" {
		h, p = "", target
	} else if strings.Count(target, ":") > 1 {
		h = strings.Trim(target, "[]") // bare IPv6 address
	}
	if h == "" {
		h = strings.TrimSpace(host)
	}
	if p == "" {
		p = defPort
	}
	if h == "" || !validHost(h) || p == "" {
		if defPort == "" {
			return "", errors.New("Host:Port erforderlich, z. B. 10.0.0.1:22")
		}
		return "", errors.New("Host oder Host:Port erforderlich, z. B. example.org:443")
	}
	if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
		return "", errors.New("Port muss zwischen 1 und 65535 liegen")
	}
	return net.JoinHostPort(h, p), nil
}

// validHost accepts IP addresses and DNS names.
func validHost(h string) bool {
	if net.ParseIP(h) != nil {
		return true
	}
	if h == "" || len(h) > 253 {
		return false
	}
	for label := range strings.SplitSeq(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return false
			}
		}
	}
	return true
}

// ms converts a duration to milliseconds with 0.1 ms resolution.
func ms(d time.Duration) float64 {
	return float64(d.Round(100*time.Microsecond)) / float64(time.Millisecond)
}

// fmtMs renders a duration for messages: "0.4 ms", "12 ms".
func fmtMs(d time.Duration) string {
	v := ms(d)
	if v < 10 {
		return strconv.FormatFloat(v, 'f', 1, 64) + " ms"
	}
	return strconv.FormatFloat(v, 'f', 0, 64) + " ms"
}

// errText translates common network errors into short German messages.
func errText(err error, timeout time.Duration) string {
	var dnsErr *net.DNSError
	var netErr net.Error
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout():
		return fmt.Sprintf("Zeitüberschreitung nach %s", fmtSeconds(timeout))
	case errors.Is(err, context.Canceled):
		return "Prüfung abgebrochen"
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return "Name nicht auflösbar: " + dnsErr.Name
	case errors.As(err, &dnsErr):
		return "DNS-Fehler: " + dnsErr.Err
	case errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(err.Error(), "refused"):
		return "Verbindung abgelehnt"
	case errors.Is(err, syscall.ECONNRESET):
		return "Verbindung zurückgesetzt"
	case errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.EHOSTDOWN):
		return "Host nicht erreichbar"
	case errors.Is(err, syscall.ENETUNREACH):
		return "Netz nicht erreichbar"
	case errors.Is(err, os.ErrPermission):
		return "Keine Berechtigung: " + err.Error()
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err.Error() // without the repeated method and URL
	}
	return err.Error()
}

func fmtSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + " s"
}
