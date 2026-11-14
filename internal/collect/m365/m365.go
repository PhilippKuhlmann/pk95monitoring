// Package m365 polls a Microsoft 365 tenant over Microsoft Graph with an app registration
// (client credentials, application permissions, read only): the tenant and its domains, the
// Entra Connect synchronisation, the licenses with their subscriptions, the secrets and
// certificates of the app registrations and the Microsoft service health. Parts the app may
// not read (missing permission) are reported in one check instead of failing the poll.
package m365

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/model"
)

// Default endpoints of the global Microsoft cloud.
const (
	DefaultLoginURL = "https://login.microsoftonline.com"
	DefaultGraphURL = "https://graph.microsoft.com"
)

const (
	requestTimeout = 30 * time.Second
	slowTTL        = 30 * time.Minute // licenses, apps and domains change rarely
	maxPages       = 50
	maxApps        = 300
	viralUnits     = 10000               // free self-service SKUs (Power Automate Free …) have this many units or more
	abandonedAfter = 30 * 24 * time.Hour // apps whose credentials all expired longer ago are left out
)

// Check groups.
const (
	groupTenant  = "Microsoft 365"
	groupLicense = "Lizenzen"
	groupApps    = "App-Registrierungen"
)

// Permissions the app registration needs (application permissions, admin consent).
var Permissions = []string{"Organization.Read.All", "Domain.Read.All", "LicenseAssignment.Read.All", "Application.Read.All", "ServiceHealth.Read.All"}

// GUIDRe matches a tenant or client ID.
var GUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Collector polls tenants; it keeps the access token and the slowly changing parts per device.
// LoginURL, GraphURL and HTTP may be replaced (demo, tests) before the first poll.
type Collector struct {
	LoginURL, GraphURL string
	HTTP               *http.Client
	now                func() time.Time

	mu   sync.Mutex
	devs map[string]*devState
}

type devState struct {
	mu      sync.Mutex // one poll per device at a time
	sig     string
	token   string
	expires time.Time
	slow    *slowData
	slowAt  time.Time
}

// New returns a Collector for the global Microsoft cloud.
func New() *Collector {
	return &Collector{LoginURL: DefaultLoginURL, GraphURL: DefaultGraphURL, HTTP: &http.Client{Timeout: requestTimeout},
		now: time.Now, devs: map[string]*devState{}}
}

// Forget drops the state of a device (deleted or changed).
func (c *Collector) Forget(deviceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.devs, deviceID)
}

func signature(cfg model.M365Config) string {
	return strings.Join([]string{cfg.TenantID, cfg.ClientID, cfg.Secret}, "\x00")
}

// ---- HTTP ----

// graphError is an answer of Graph with an error status.
type graphError struct {
	status int
	code   string
	msg    string
}

func (e *graphError) Error() string {
	switch e.status {
	case http.StatusUnauthorized:
		return "Microsoft Graph lehnt das Token ab (" + e.code + ")"
	case http.StatusForbidden:
		return "Berechtigung fehlt"
	case http.StatusTooManyRequests:
		return "Microsoft Graph drosselt die Abfragen (HTTP 429) – beim nächsten Mal wieder"
	}
	if e.msg != "" {
		return fmt.Sprintf("Microsoft Graph: HTTP %d – %s", e.status, e.msg)
	}
	return fmt.Sprintf("Microsoft Graph antwortet mit HTTP %d", e.status)
}

func forbidden(err error) bool {
	var ge *graphError
	return errors.As(err, &ge) && ge.status == http.StatusForbidden
}

// tokenError turns an Entra ID sign-in error (AADSTS code) into German.
func tokenError(status int, body []byte) error {
	var e struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
		Codes       []int  `json:"error_codes"`
	}
	_ = json.Unmarshal(body, &e)
	code := 0
	if len(e.Codes) > 0 {
		code = e.Codes[0]
	}
	switch code {
	case 90002, 900023, 90024:
		return errors.New("Mandant nicht gefunden – Mandanten-ID (Verzeichnis-ID) prüfen")
	case 700016:
		return errors.New("App nicht gefunden – Anwendungs-ID (Client-ID) prüfen; ist die App in diesem Mandanten registriert?")
	case 7000215:
		return errors.New("Geheimer Schlüssel ungültig – den Wert (nicht die ID) des Geheimnisses eintragen")
	case 7000222:
		return errors.New("Geheimer Schlüssel abgelaufen – in Entra ID ein neues Geheimnis anlegen und hier eintragen")
	case 700024, 7000112:
		return errors.New("App im Mandanten deaktiviert")
	}
	if e.Error != "" {
		d, _, _ := strings.Cut(e.Description, "\r\n")
		return fmt.Errorf("Anmeldung bei Microsoft fehlgeschlagen: %s", strings.TrimSpace(d))
	}
	return fmt.Errorf("Anmeldung bei Microsoft fehlgeschlagen (HTTP %d)", status)
}

func friendly(err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "Client.Timeout") || strings.Contains(s, "i/o timeout"):
		return errors.New("Zeitüberschreitung – ist Microsoft 365 (login.microsoftonline.com, graph.microsoft.com) vom Server aus erreichbar?")
	case strings.Contains(s, "no such host"):
		return errors.New("Microsoft 365 nicht erreichbar – DNS-Auflösung fehlgeschlagen")
	}
	return err
}

// token returns a valid access token, fetching a new one when needed.
func (c *Collector) token(ctx context.Context, st *devState, cfg model.M365Config) (string, error) {
	if st.token != "" && c.now().Before(st.expires) {
		return st.token, nil
	}
	tok, ttl, err := fetchToken(ctx, c.HTTP, c.LoginURL, c.GraphURL, cfg)
	if err != nil {
		return "", err
	}
	st.token, st.expires = tok, c.now().Add(ttl-5*time.Minute)
	return tok, nil
}

func fetchToken(ctx context.Context, hc *http.Client, loginURL, graphURL string, cfg model.M365Config) (string, time.Duration, error) {
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {cfg.ClientID}, "client_secret": {cfg.Secret},
		"scope": {strings.TrimRight(graphURL, "/") + "/.default"}}
	u := strings.TrimRight(loginURL, "/") + "/" + url.PathEscape(cfg.TenantID) + "/oauth2/v2.0/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return "", 0, friendly(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", 0, tokenError(resp.StatusCode, body)
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &t); err != nil || t.AccessToken == "" {
		return "", 0, errors.New("Anmeldung bei Microsoft: keine gültige Antwort")
	}
	return t.AccessToken, time.Duration(max(t.ExpiresIn, 600)) * time.Second, nil
}

type client struct {
	hc    *http.Client
	base  string
	token string
}

// get reads one Graph resource (a path on the Graph URL, or a nextLink on it).
func (cl *client) get(ctx context.Context, path string, out any) error {
	u := path
	if !strings.HasPrefix(u, cl.base+"/") {
		if strings.Contains(u, "://") {
			return errors.New("Microsoft Graph: unerwarteter Folge-Link")
		}
		u = cl.base + path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cl.token)
	req.Header.Set("Accept", "application/json")
	resp, err := cl.hc.Do(req)
	if err != nil {
		return friendly(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return friendly(err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		return &graphError{status: resp.StatusCode, code: e.Error.Code, msg: e.Error.Message}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("Microsoft Graph: unerwartete Antwort (%v)", err)
	}
	return nil
}

// list reads every page of a collection.
func list[T any](ctx context.Context, cl *client, path string) ([]T, error) {
	var all []T
	for range maxPages {
		var page struct {
			Value []T    `json:"value"`
			Next  string `json:"@odata.nextLink"`
		}
		if err := cl.get(ctx, path, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Value...)
		if page.Next == "" {
			break
		}
		path = page.Next
	}
	return all, nil
}

// ---- Graph resources ----

type organization struct {
	ID               string `json:"id"`
	DisplayName      string `json:"displayName"`
	Country          string `json:"countryLetterCode"`
	SyncEnabled      *bool  `json:"onPremisesSyncEnabled"`
	LastSync         string `json:"onPremisesLastSyncDateTime"`
	LastPasswordSync string `json:"onPremisesLastPasswordSyncDateTime"`
	VerifiedDomains  []struct {
		Name      string `json:"name"`
		IsDefault bool   `json:"isDefault"`
	} `json:"verifiedDomains"`
}

type domain struct {
	ID         string `json:"id"`
	IsVerified bool   `json:"isVerified"`
	IsDefault  bool   `json:"isDefault"`
}

type sku struct {
	SkuID         string `json:"skuId"`
	PartNumber    string `json:"skuPartNumber"`
	Status        string `json:"capabilityStatus"` // Enabled, Warning, Suspended, Deleted, LockedOut
	AppliesTo     string `json:"appliesTo"`
	ConsumedUnits int    `json:"consumedUnits"`
	Prepaid       struct {
		Enabled   int `json:"enabled"`
		Suspended int `json:"suspended"`
		Warning   int `json:"warning"`
		LockedOut int `json:"lockedOut"`
	} `json:"prepaidUnits"`
}

type subscription struct {
	SkuID         string `json:"skuId"`
	Status        string `json:"status"` // Enabled, Expired, Suspended, Warning, LockedOut
	IsTrial       bool   `json:"isTrial"`
	NextLifecycle string `json:"nextLifecycleDateTime"`
}

type credential struct {
	DisplayName string `json:"displayName"`
	End         string `json:"endDateTime"`
}

type application struct {
	AppID       string       `json:"appId"`
	DisplayName string       `json:"displayName"`
	Passwords   []credential `json:"passwordCredentials"`
	Keys        []credential `json:"keyCredentials"`
}

type healthOverview struct {
	Service string `json:"service"`
	Status  string `json:"status"`
}

type issue struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Service        string `json:"service"`
	Classification string `json:"classification"` // incident, advisory
	Start          string `json:"startDateTime"`
}

// slowData are the parts read every slowTTL; denied lists the areas without permission.
type slowData struct {
	domains []domain
	skus    []sku
	subs    []subscription
	apps    []application
	denied  []string
}

func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil && t.Year() > 1
}

// ---- Collect ----

// Collect polls one tenant. It returns an error when the sign-in fails or the tenant cannot
// be read; areas without permission are listed in the check "Berechtigungen".
func (c *Collector) Collect(ctx context.Context, d model.Device) ([]model.Check, error) {
	if d.M365 == nil || d.M365.TenantID == "" || d.M365.ClientID == "" || d.M365.Secret == "" {
		return nil, errors.New("Zugang zu Microsoft 365 fehlt (Mandanten-ID, Anwendungs-ID und geheimer Schlüssel)")
	}
	cfg := *d.M365
	c.mu.Lock()
	st := c.devs[d.ID]
	if st == nil || st.sig != signature(cfg) {
		st = &devState{sig: signature(cfg)}
		c.devs[d.ID] = st
	}
	c.mu.Unlock()
	st.mu.Lock()
	defer st.mu.Unlock()

	tok, err := c.token(ctx, st, cfg)
	if err != nil {
		return nil, err
	}
	cl := &client{hc: c.HTTP, base: strings.TrimRight(c.GraphURL, "/"), token: tok}
	orgs, err := list[organization](ctx, cl, "/v1.0/organization")
	if err != nil {
		var ge *graphError
		if errors.As(err, &ge) && ge.status == http.StatusUnauthorized {
			st.token = "" // fetch a new token next time
		}
		if forbidden(err) {
			return nil, errors.New("Keine Berechtigung, den Mandanten zu lesen – der App fehlt „Organization.Read.All“ (Anwendungsberechtigung mit Administratorzustimmung)")
		}
		return nil, err
	}
	if len(orgs) == 0 {
		return nil, errors.New("Microsoft Graph liefert keinen Mandanten")
	}
	org := orgs[0]
	now := c.now()

	if st.slow == nil || now.Sub(st.slowAt) >= slowTTL {
		st.slow, st.slowAt = readSlow(ctx, cl), now
	}
	sd := st.slow
	denied := slices.Clone(sd.denied)

	checks := []model.Check{tenantCheck(org, sd.domains)}
	if ch, ok := syncCheck(org, now); ok {
		checks = append(checks, ch)
	}
	checks = append(checks, licenseChecks(sd.skus, sd.subs, now)...)
	checks = append(checks, appChecks(sd.apps, cfg.ClientID, now)...)

	overviews, err := list[healthOverview](ctx, cl, "/v1.0/admin/serviceAnnouncement/healthOverviews")
	if err == nil {
		var issues []issue
		issues, err = list[issue](ctx, cl, "/v1.0/admin/serviceAnnouncement/issues?$filter="+url.QueryEscape("isResolved eq false"))
		if err == nil {
			checks = append(checks, healthCheck(overviews, issues))
		}
	}
	switch {
	case forbidden(err):
		denied = append(denied, "Dienststatus")
	case err != nil:
		checks = append(checks, model.Check{Key: "m365:health", Name: "Microsoft-Dienststatus", Group: groupTenant, State: model.StateUnknown, Message: err.Error()})
	}
	return append(checks, permCheck(denied)), nil
}

// readSlow reads domains, licenses, subscriptions and apps; areas without permission are
// listed in denied, other errors leave the area empty.
func readSlow(ctx context.Context, cl *client) *slowData {
	sd := &slowData{}
	deny := func(area string, err error) {
		if forbidden(err) {
			sd.denied = append(sd.denied, area)
		}
	}
	var err error
	if sd.domains, err = list[domain](ctx, cl, "/v1.0/domains"); err != nil {
		deny("Domains", err)
	}
	if sd.skus, err = list[sku](ctx, cl, "/v1.0/subscribedSkus"); err != nil {
		deny("Lizenzen", err)
	}
	if sd.subs, err = list[subscription](ctx, cl, "/v1.0/directory/subscriptions"); err != nil {
		deny("Abonnements", err)
	}
	if sd.apps, err = list[application](ctx, cl, "/v1.0/applications?$select=appId,displayName,passwordCredentials,keyCredentials&$top=999"); err != nil {
		deny("App-Registrierungen", err)
	}
	return sd
}

// areaPermission is the permission each area needs.
var areaPermission = map[string]string{
	"Domains": "Domain.Read.All", "Lizenzen": "Organization.Read.All", "Abonnements": "LicenseAssignment.Read.All",
	"App-Registrierungen": "Application.Read.All", "Dienststatus": "ServiceHealth.Read.All",
}

func permCheck(denied []string) model.Check {
	ch := model.Check{Key: "m365:perm", Name: "Berechtigungen", Group: groupTenant, State: model.StateOK,
		Message: "Alle Bereiche lesbar", Facts: map[string]string{"Benötigt": strings.Join(Permissions, ", ")}}
	if len(denied) == 0 {
		return ch
	}
	parts := make([]string, 0, len(denied))
	for _, a := range denied {
		parts = append(parts, fmt.Sprintf("%s (%s)", a, areaPermission[a]))
	}
	ch.State = model.StateWarn
	ch.Message = "Nicht lesbar: " + strings.Join(parts, ", ") + " – Anwendungsberechtigung ergänzen und Administratorzustimmung erteilen"
	return ch
}

func tenantCheck(org organization, domains []domain) model.Check {
	ch := model.Check{Key: "m365:tenant", Name: "Mandant", Group: groupTenant, State: model.StateOK,
		Facts: map[string]string{"Mandanten-ID": org.ID}}
	if org.Country != "" {
		ch.Facts["Land"] = org.Country
	}
	var verified, unverified []string
	def := ""
	if len(domains) > 0 {
		for _, dm := range domains {
			if !dm.IsVerified {
				unverified = append(unverified, dm.ID)
				continue
			}
			verified = append(verified, dm.ID)
			if dm.IsDefault {
				def = dm.ID
			}
		}
	} else { // without Domain.Read.All: the verified domains of the organization
		for _, dm := range org.VerifiedDomains {
			verified = append(verified, dm.Name)
			if dm.IsDefault {
				def = dm.Name
			}
		}
	}
	slices.Sort(verified)
	slices.Sort(unverified)
	if def != "" {
		ch.Facts["Standarddomain"] = def
	}
	ch.Facts["Domains"] = strings.Join(verified, ", ")
	parts := []string{org.DisplayName}
	if def != "" {
		parts = append(parts, def)
	}
	parts = append(parts, fmt.Sprintf("%d %s", len(verified), plural(len(verified), "Domain", "Domains")))
	ch.Message = strings.Join(parts, " · ")
	if len(unverified) > 0 {
		ch.State = model.StateWarn
		ch.Facts["Nicht verifiziert"] = strings.Join(unverified, ", ")
		ch.Message = fmt.Sprintf("%s nicht verifiziert · %s", strings.Join(unverified, ", "), ch.Message)
	}
	return ch
}

// syncCheck is the Entra Connect (or Cloud Sync) synchronisation; ok is false for a
// cloud-only tenant.
func syncCheck(org organization, now time.Time) (model.Check, bool) {
	if org.SyncEnabled == nil || !*org.SyncEnabled {
		return model.Check{}, false
	}
	ch := model.Check{Key: "m365:sync", Name: "Entra Connect Sync", Group: groupTenant, State: model.StateOK, Facts: map[string]string{}}
	last, ok := parseTime(org.LastSync)
	if !ok {
		ch.State, ch.Message = model.StateWarn, "Synchronisierung eingeschaltet, aber noch nie gelaufen"
		return ch, true
	}
	ch.Metrics = []model.Metric{{Name: "m365.sync_age", Value: round1(max(now.Sub(last).Hours(), 0)), Unit: "h"}}
	ch.Facts["Letzte Synchronisierung"] = last.Local().Format("02.01.2006 15:04")
	ch.Message = "letzte Synchronisierung " + ago(now.Sub(last))
	if pw, ok := parseTime(org.LastPasswordSync); ok {
		ch.Facts["Letzte Kennwort-Synchronisierung"] = pw.Local().Format("02.01.2006 15:04")
	}
	return ch, true
}

// licenseChecks lists the paid licenses, one check per product (SKU).
func licenseChecks(skus []sku, subs []subscription, now time.Time) []model.Check {
	bySku := map[string][]subscription{}
	for _, s := range subs {
		k := strings.ToLower(s.SkuID)
		bySku[k] = append(bySku[k], s)
	}
	var out []model.Check
	for _, s := range skus {
		total := s.Prepaid.Enabled + s.Prepaid.Warning
		if s.Status == "Deleted" || (s.AppliesTo != "" && s.AppliesTo != "User") || total >= viralUnits ||
			(total == 0 && s.ConsumedUnits == 0) {
			continue
		}
		ch := model.Check{Key: "m365:sku:" + strings.ToLower(s.PartNumber), Name: SkuName(s.PartNumber), Group: groupLicense,
			State: model.StateOK, Facts: map[string]string{"Produkt": s.PartNumber}}
		free := total - s.ConsumedUnits
		ch.Metrics = []model.Metric{{Name: "m365.lic_free", Value: float64(free)}}
		ch.Message = fmt.Sprintf("%d von %d zugewiesen · %d frei", s.ConsumedUnits, total, max(free, 0))
		if s.Prepaid.Warning > 0 {
			ch.Facts["In Kulanzzeit"] = fmt.Sprint(s.Prepaid.Warning)
		}
		var next time.Time
		trial, ending := false, false
		for _, sub := range bySku[strings.ToLower(s.SkuID)] {
			if t, ok := parseTime(sub.NextLifecycle); ok && (next.IsZero() || t.Before(next)) {
				next = t
			}
			trial = trial || sub.IsTrial
			ending = ending || sub.Status == "Warning" || sub.Status == "Expired" || sub.Status == "Suspended"
		}
		if !next.IsZero() {
			ch.Facts["Endet ohne Verlängerung"] = next.Local().Format("02.01.2006")
		}
		if trial {
			ch.Facts["Testversion"] = "ja"
		}
		switch s.Status {
		case "Suspended", "LockedOut":
			ch.State, ch.Message = model.StateCrit, "Abonnement gesperrt – Lizenzen nicht nutzbar · "+ch.Message
		case "Warning":
			ch.State, ch.Message = model.StateWarn, "Abonnement abgelaufen, Kulanzzeit läuft – bitte verlängern · "+ch.Message
		}
		if free < 0 && ch.State == model.StateOK {
			ch.State, ch.Message = model.StateWarn, fmt.Sprintf("%d mehr zugewiesen als vorhanden · %s", -free, ch.Message)
		}
		// Only trials and ending subscriptions get a countdown: for the others the date is
		// the next (usually automatic) renewal.
		if (trial || ending || s.Status != "Enabled") && !next.IsZero() {
			ch.Metrics = append(ch.Metrics, model.Metric{Name: "m365.sub_days", Value: round1(next.Sub(now).Hours() / 24), Unit: "d"})
			if trial && ch.State == model.StateOK {
				ch.Message = fmt.Sprintf("Testversion bis %s · %s", next.Local().Format("02.01.2006"), ch.Message)
			}
		}
		out = append(out, ch)
	}
	slices.SortFunc(out, func(a, b model.Check) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// appChecks judges the secrets and certificates of the app registrations by the credential
// valid longest (while one is valid the app works). Apps without credentials and apps whose
// credentials expired more than 30 days ago are left out.
func appChecks(apps []application, self string, now time.Time) []model.Check {
	type cred struct {
		kind, name string
		end        time.Time
	}
	var out []model.Check
	for _, a := range apps {
		var creds []cred
		for _, p := range a.Passwords {
			if t, ok := parseTime(p.End); ok {
				creds = append(creds, cred{"Geheimnis", p.DisplayName, t})
			}
		}
		for _, k := range a.Keys {
			if t, ok := parseTime(k.End); ok {
				creds = append(creds, cred{"Zertifikat", k.DisplayName, t})
			}
		}
		if len(creds) == 0 {
			continue
		}
		slices.SortFunc(creds, func(x, y cred) int { return y.end.Compare(x.end) })
		best := creds[0]
		if now.Sub(best.end) > abandonedAfter {
			continue
		}
		name := a.DisplayName
		if name == "" {
			name = a.AppID
		}
		ch := model.Check{Key: "m365:app:" + strings.ToLower(a.AppID), Name: name, Group: groupApps, State: model.StateOK,
			Facts: map[string]string{"Anwendungs-ID": a.AppID}}
		if strings.EqualFold(a.AppID, self) {
			ch.Facts["Hinweis"] = "Mit dieser App fragt das Monitoring Microsoft 365 ab – läuft ihr Geheimnis ab, endet die Überwachung."
		}
		days := best.end.Sub(now).Hours() / 24
		ch.Metrics = []model.Metric{{Name: "m365.cred_days", Value: round1(days), Unit: "d"}}
		label := func(c cred) string {
			if c.name != "" {
				return c.kind + " „" + c.name + "“"
			}
			return c.kind
		}
		until := best.end.Local().Format("02.01.2006")
		ch.Facts["Gültig bis"] = until
		if days < 0 {
			ch.Message = fmt.Sprintf("%s abgelaufen am %s", label(best), until)
		} else {
			d := int(days)
			ch.Message = fmt.Sprintf("%s gültig bis %s (noch %d %s)", label(best), until, d, plural(d, "Tag", "Tage"))
		}
		for i, c := range creds {
			if i == 5 {
				break
			}
			ch.Facts[label(c)] = c.end.Local().Format("02.01.2006")
		}
		out = append(out, ch)
		if len(out) >= maxApps {
			break
		}
	}
	slices.SortFunc(out, func(a, b model.Check) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

// healthBad maps the service health states that are a problem (serviceHealthStatus).
var healthBad = map[string]struct{ state, word string }{
	"serviceInterruption": {model.StateCrit, "ausgefallen"},
	"serviceDegradation":  {model.StateWarn, "eingeschränkt"},
	"extendedRecovery":    {model.StateWarn, "Wiederherstellung dauert an"},
	"investigating":       {model.StateWarn, "Störung wird untersucht"},
	"restoringService":    {model.StateWarn, "wird wiederhergestellt"},
	"verifyingService":    {model.StateWarn, "Behebung wird geprüft"},
}

// healthCheck summarises the Microsoft service health: services with a problem and their
// open incidents; advisories are only counted.
func healthCheck(ovs []healthOverview, issues []issue) model.Check {
	ch := model.Check{Key: "m365:health", Name: "Microsoft-Dienststatus", Group: groupTenant, State: model.StateOK, Facts: map[string]string{}}
	incidents := map[string][]issue{}
	advisories := 0
	for _, is := range issues {
		switch is.Classification {
		case "incident":
			incidents[is.Service] = append(incidents[is.Service], is)
		case "advisory":
			advisories++
		}
	}
	var bad []string
	for _, o := range ovs {
		h, ok := healthBad[o.Status]
		if !ok {
			continue
		}
		if model.Rank(h.state) > model.Rank(ch.State) {
			ch.State = h.state
		}
		text := o.Service + " " + h.word
		if l := incidents[o.Service]; len(l) > 0 {
			slices.SortFunc(l, func(a, b issue) int { return strings.Compare(a.Start, b.Start) })
			ch.Facts[o.Service] = l[0].ID + ": " + l[0].Title
			text += " (" + l[0].ID + ")"
		}
		bad = append(bad, text)
	}
	slices.Sort(bad)
	if len(bad) > 0 {
		ch.Message = strings.Join(bad, " · ")
	} else {
		ch.Message = fmt.Sprintf("Alle %d Dienste in Betrieb", len(ovs))
		if advisories > 0 {
			ch.Message += fmt.Sprintf(" · %d %s von Microsoft", advisories, plural(advisories, "Hinweis", "Hinweise"))
		}
	}
	if advisories > 0 {
		ch.Facts["Hinweise von Microsoft"] = fmt.Sprint(advisories)
	}
	return ch
}

// ---- helpers ----

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "gerade eben"
	case d < time.Hour:
		return fmt.Sprintf("vor %d Min.", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("vor %d Std.", int(d.Hours()))
	}
	return fmt.Sprintf("vor %d Tagen", int(d.Hours()/24))
}

// skuNames are the product names of common SKUs (Microsoft's list "Product names and service
// plan identifiers for licensing").
var skuNames = map[string]string{
	"O365_BUSINESS_ESSENTIALS": "Microsoft 365 Business Basic", "O365_BUSINESS_PREMIUM": "Microsoft 365 Business Standard",
	"SPB": "Microsoft 365 Business Premium", "O365_BUSINESS": "Microsoft 365 Apps for Business",
	"OFFICESUBSCRIPTION": "Microsoft 365 Apps for Enterprise", "SPE_E3": "Microsoft 365 E3", "SPE_E5": "Microsoft 365 E5",
	"SPE_F1": "Microsoft 365 F3", "ENTERPRISEPACK": "Office 365 E3", "ENTERPRISEPREMIUM": "Office 365 E5",
	"STANDARDPACK": "Office 365 E1", "DESKLESSPACK": "Office 365 F3", "EXCHANGESTANDARD": "Exchange Online (Plan 1)",
	"EXCHANGEENTERPRISE": "Exchange Online (Plan 2)", "EXCHANGEDESKLESS": "Exchange Online Kiosk",
	"EXCHANGEARCHIVE_ADDON": "Exchange Online Archiving", "AAD_PREMIUM": "Microsoft Entra ID P1",
	"AAD_PREMIUM_P2": "Microsoft Entra ID P2", "EMS": "Enterprise Mobility + Security E3", "EMSPREMIUM": "Enterprise Mobility + Security E5",
	"INTUNE_A": "Microsoft Intune Plan 1", "ATP_ENTERPRISE": "Microsoft Defender für Office 365 (Plan 1)",
	"THREAT_INTELLIGENCE": "Microsoft Defender für Office 365 (Plan 2)", "DEFENDER_ENDPOINT_P1": "Microsoft Defender für Endpunkt P1",
	"MDATP_XPLAT": "Microsoft Defender für Endpunkt P2", "MCOEV": "Teams Phone Standard", "MCOMEETADV": "Microsoft 365 Audiokonferenz",
	"Microsoft_Teams_Premium": "Teams Premium", "TEAMS_ESSENTIALS_AAD": "Teams Essentials", "Microsoft_Teams_Rooms_Pro": "Teams Rooms Pro",
	"PROJECTPROFESSIONAL": "Project Plan 3", "PROJECT_P1": "Project Plan 1", "VISIOCLIENT": "Visio Plan 2", "VISIO_PLAN1_DEPT": "Visio Plan 1",
	"POWER_BI_PRO": "Power BI Pro", "POWER_BI_PREMIUM_PER_USER": "Power BI Premium pro Benutzer", "Microsoft_365_Copilot": "Microsoft 365 Copilot",
	"WIN10_VDA_E3": "Windows Enterprise E3", "Win10_VDA_E5": "Windows Enterprise E5",
}

// SkuName is the product name of a SKU (the part number when unknown).
func SkuName(part string) string {
	if n, ok := skuNames[part]; ok {
		return n
	}
	return part
}

// ---- connection test ----

// TestResult is the answer of a connection test.
type TestResult struct {
	Tenant   string   `json:"tenant"`
	Domain   string   `json:"domain"`
	Sync     bool     `json:"sync"`     // Entra Connect synchronises from an on-premises AD
	Licenses int      `json:"licenses"` // paid license products
	Apps     int      `json:"apps"`     // app registrations with secrets or certificates
	Missing  []string `json:"missing"`  // permissions without consent
}

// Test signs in, reads the tenant and tries every area.
func (c *Collector) Test(ctx context.Context, cfg model.M365Config) (TestResult, error) {
	tok, _, err := fetchToken(ctx, c.HTTP, c.LoginURL, c.GraphURL, cfg)
	if err != nil {
		return TestResult{}, err
	}
	cl := &client{hc: c.HTTP, base: strings.TrimRight(c.GraphURL, "/"), token: tok}
	orgs, err := list[organization](ctx, cl, "/v1.0/organization")
	if err != nil {
		if forbidden(err) {
			return TestResult{}, errors.New("Anmeldung klappt, aber die App darf den Mandanten nicht lesen – „Organization.Read.All“ als Anwendungsberechtigung hinzufügen und Administratorzustimmung erteilen")
		}
		return TestResult{}, err
	}
	if len(orgs) == 0 {
		return TestResult{}, errors.New("Microsoft Graph liefert keinen Mandanten")
	}
	org := orgs[0]
	res := TestResult{Tenant: org.DisplayName, Sync: org.SyncEnabled != nil && *org.SyncEnabled, Missing: []string{}}
	for _, dm := range org.VerifiedDomains {
		if dm.IsDefault {
			res.Domain = dm.Name
		}
	}
	sd := readSlow(ctx, cl)
	now := c.now()
	res.Licenses = len(licenseChecks(sd.skus, sd.subs, now))
	res.Apps = len(appChecks(sd.apps, cfg.ClientID, now))
	denied := sd.denied
	if _, err := list[healthOverview](ctx, cl, "/v1.0/admin/serviceAnnouncement/healthOverviews"); forbidden(err) {
		denied = append(denied, "Dienststatus")
	}
	for _, a := range denied {
		res.Missing = append(res.Missing, areaPermission[a])
	}
	return res, nil
}
