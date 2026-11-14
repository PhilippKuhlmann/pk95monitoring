package netcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"

	"pk95monitoring/internal/model"
)

// Domain checks ask the registry over RDAP (the successor of whois) when a domain expires.
// The RDAP server of a top-level domain comes from the IANA bootstrap file. Registries
// change these dates rarely, so results are cached (domainTTL; failures domainRetry) and a
// check polled every minute asks the registry about twice a day.

var (
	// rdapBootstrapURL lists the RDAP servers per top-level domain. Replaced in tests.
	rdapBootstrapURL = "https://data.iana.org/rdap/dns.json"
	rdapClient       = &http.Client{Timeout: 20 * time.Second}
	// rdapFallback are RDAP servers of registries missing from the IANA file. DENIC and
	// SWITCH answer, but publish no expiry date.
	rdapFallback = map[string]string{"de": "https://rdap.denic.de/", "ch": "https://rdap.nic.ch/", "li": "https://rdap.nic.ch/"}
)

const (
	domainTTL    = 12 * time.Hour
	domainRetry  = time.Hour
	bootstrapTTL = 24 * time.Hour
	maxRDAPBody  = 1 << 20
)

// domainInfo is what the registry tells about a domain.
type domainInfo struct {
	Name       string
	Registered bool
	Expires    time.Time // zero = not published (e.g. .de)
	Created    time.Time
	Registrar  string
	Status     []string
	Server     string // RDAP host
}

type domainEntry struct {
	at   time.Time
	info domainInfo
	err  error
}

var rdapCache = struct {
	sync.Mutex
	domains   map[string]domainEntry
	bootstrap map[string]string // TLD → base URL
	bootAt    time.Time
}{domains: map[string]domainEntry{}}

// SetRDAPBootstrap replaces the IANA bootstrap URL (the demo serves its own registry) and
// clears the cache.
func SetRDAPBootstrap(u string) {
	rdapCache.Lock()
	defer rdapCache.Unlock()
	rdapBootstrapURL = u
	rdapCache.domains, rdapCache.bootstrap = map[string]domainEntry{}, nil
}

// domainName returns the registrable domain of a target (a name, host or URL); empty = the
// device host.
func domainName(target, host string) (string, error) {
	t := strings.TrimSpace(target)
	if t == "" {
		t = strings.TrimSpace(host)
	}
	if strings.Contains(t, "://") {
		if u, err := url.Parse(t); err == nil {
			t = u.Hostname()
		}
	}
	t = strings.ToLower(strings.TrimSuffix(t, "."))
	if t == "" || net.ParseIP(t) != nil || !strings.Contains(t, ".") || !validHost(t) {
		return "", errors.New("Domain angeben, z. B. example.org")
	}
	d, err := publicsuffix.EffectiveTLDPlusOne(t)
	if err != nil {
		return "", errors.New("Keine registrierbare Domain: " + t)
	}
	return d, nil
}

// runDomain checks when the domain of the target expires.
func runDomain(ctx context.Context, res *model.Check, c model.NetCheck, host string) {
	name, err := domainName(c.Target, host)
	if err != nil {
		res.Message = err.Error()
		return
	}
	info, err := lookupDomain(ctx, name)
	if err != nil {
		res.State, res.Message = model.StateUnknown, err.Error()
		return
	}
	res.Facts = map[string]string{"Domain": info.Name, "RDAP-Server": info.Server}
	if !info.Registered {
		res.State, res.Message = model.StateCrit, "Domain ist nicht registriert"
		return
	}
	if info.Registrar != "" {
		res.Facts["Registrar"] = info.Registrar
	}
	if !info.Created.IsZero() {
		res.Facts["Registriert seit"] = info.Created.Local().Format("02.01.2006")
	}
	if len(info.Status) > 0 {
		res.Facts["Status"] = strings.Join(info.Status, ", ")
	}
	res.State = model.StateOK
	if !info.Expires.IsZero() {
		days := math.Floor(time.Until(info.Expires).Hours() / 24)
		res.Metrics = append(res.Metrics, model.Metric{Name: "domain.days", Value: days, Unit: "d"})
		res.Facts["Läuft ab"] = info.Expires.Local().Format("02.01.2006")
		if days < 0 {
			res.State, res.Message = model.StateCrit, "Abgelaufen am "+info.Expires.Local().Format("02.01.2006")
		} else {
			res.Message = fmt.Sprintf("Registriert bis %s (noch %.0f Tage)", info.Expires.Local().Format("02.01.2006"), days)
		}
	} else {
		res.Message = "Registriert – die Registry veröffentlicht kein Ablaufdatum"
		if strings.HasSuffix(info.Name, ".de") {
			res.Message += " (.de-Domains verlängern sich automatisch, solange der Provider sie bezahlt)"
		}
	}
	if s := holdStatus(info.Status); s != "" {
		res.State, res.Message = model.StateCrit, s
	}
}

// holdStatus translates registry states that take a domain offline.
func holdStatus(status []string) string {
	for _, s := range status {
		switch strings.ToLower(s) {
		case "client hold", "server hold":
			return "Domain ist gesperrt (" + s + ") – sie wird nicht mehr aufgelöst"
		case "redemption period":
			return "Domain ist gekündigt oder abgelaufen (Redemption Period) – sie kann nur noch kostenpflichtig wiederhergestellt werden"
		case "pending delete":
			return "Domain wird gelöscht (Pending Delete)"
		}
	}
	return ""
}

// lookupDomain returns the cached registry data of a domain, asking the registry when the
// cache entry is too old.
func lookupDomain(ctx context.Context, name string) (domainInfo, error) {
	rdapCache.Lock()
	e, ok := rdapCache.domains[name]
	rdapCache.Unlock()
	if ok {
		ttl := domainTTL
		if e.err != nil {
			ttl = domainRetry
		}
		if time.Since(e.at) < ttl {
			return e.info, e.err
		}
	}
	info, err := queryRDAP(ctx, name)
	if err != nil && errors.Is(ctx.Err(), context.Canceled) {
		return info, err // do not cache an aborted lookup
	}
	rdapCache.Lock()
	rdapCache.domains[name] = domainEntry{at: time.Now(), info: info, err: err}
	rdapCache.Unlock()
	return info, err
}

// rdapServer returns the RDAP base URL for a domain from the (cached) IANA bootstrap file.
func rdapServer(ctx context.Context, name string) (string, error) {
	rdapCache.Lock()
	boot, at, bootURL := rdapCache.bootstrap, rdapCache.bootAt, rdapBootstrapURL
	rdapCache.Unlock()
	if boot == nil || time.Since(at) > bootstrapTTL {
		var doc struct {
			Services [][][]string `json:"services"`
		}
		if err := getJSON(ctx, bootURL, &doc); err != nil {
			if boot == nil {
				return "", fmt.Errorf("RDAP-Verzeichnis der IANA nicht erreichbar: %w", err)
			}
		} else {
			boot = map[string]string{}
			for _, s := range doc.Services {
				if len(s) < 2 || len(s[1]) == 0 {
					continue
				}
				base := s[1][0]
				for _, u := range s[1] {
					if strings.HasPrefix(u, "https://") {
						base = u
						break
					}
				}
				for _, tld := range s[0] {
					boot[strings.ToLower(tld)] = base
				}
			}
			rdapCache.Lock()
			rdapCache.bootstrap, rdapCache.bootAt = boot, time.Now()
			rdapCache.Unlock()
		}
	}
	labels := strings.Split(name, ".")
	for _, m := range []map[string]string{boot, rdapFallback} {
		for i := 1; i < len(labels); i++ { // longest suffix first ("co.uk" before "uk")
			if base, ok := m[strings.Join(labels[i:], ".")]; ok {
				return base, nil
			}
		}
	}
	return "", fmt.Errorf("Für .%s gibt es keinen RDAP-Dienst – der Ablauf ist nicht abfragbar", labels[len(labels)-1])
}

// queryRDAP asks the registry of the domain.
func queryRDAP(ctx context.Context, name string) (domainInfo, error) {
	base, err := rdapServer(ctx, name)
	if err != nil {
		return domainInfo{}, err
	}
	u := strings.TrimSuffix(base, "/") + "/domain/" + url.PathEscape(name)
	info := domainInfo{Name: name}
	if pu, err := url.Parse(u); err == nil {
		info.Server = pu.Host
	}
	var doc rdapDomain
	err = getJSON(ctx, u, &doc)
	var se statusError
	switch {
	case errors.As(err, &se) && se.code == http.StatusNotFound:
		return info, nil // not registered
	case err != nil:
		return domainInfo{}, fmt.Errorf("Abfrage bei %s fehlgeschlagen: %w", info.Server, err)
	}
	info.Registered = true
	for _, ev := range doc.Events {
		t, err := time.Parse(time.RFC3339, ev.Date)
		if err != nil {
			continue
		}
		switch strings.ToLower(ev.Action) {
		case "expiration", "registrar expiration":
			if info.Expires.IsZero() || t.Before(info.Expires) {
				info.Expires = t
			}
		case "registration":
			info.Created = t
		}
	}
	for _, s := range doc.Status {
		if !slices.Contains(info.Status, s) {
			info.Status = append(info.Status, s)
		}
	}
	info.Registrar = registrarName(doc.Entities)
	return info, nil
}

type rdapDomain struct {
	Status []string `json:"status"`
	Events []struct {
		Action string `json:"eventAction"`
		Date   string `json:"eventDate"`
	} `json:"events"`
	Entities []rdapEntity `json:"entities"`
}

type rdapEntity struct {
	Roles    []string        `json:"roles"`
	VCard    json.RawMessage `json:"vcardArray"`
	Entities []rdapEntity    `json:"entities"`
}

// registrarName is the "fn" of the vCard of the entity with the role registrar.
func registrarName(list []rdapEntity) string {
	for _, e := range list {
		if slices.Contains(e.Roles, "registrar") {
			var vc []any
			if json.Unmarshal(e.VCard, &vc) == nil && len(vc) == 2 {
				props, _ := vc[1].([]any)
				for _, p := range props {
					if f, ok := p.([]any); ok && len(f) >= 4 && f[0] == "fn" {
						if s, ok := f[3].(string); ok {
							return s
						}
					}
				}
			}
		}
		if n := registrarName(e.Entities); n != "" {
			return n
		}
	}
	return ""
}

type statusError struct{ code int }

func (e statusError) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

// getJSON fetches and decodes a JSON document.
func getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/rdap+json, application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := rdapClient.Do(req)
	if err != nil {
		return errors.New(errText(err, rdapClient.Timeout))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError{resp.StatusCode}
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxRDAPBody)).Decode(v)
}
