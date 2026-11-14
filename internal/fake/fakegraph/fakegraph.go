// Package fakegraph simulates the parts of Microsoft Entra ID (token endpoint) and Microsoft
// Graph that internal/collect/m365 reads, for tests and the demo: the tenant "Muster GmbH"
// with Entra Connect, four domains (one unverified), licenses (one in its grace period, a
// Copilot trial ending soon), app registrations with expiring secrets and an Exchange Online
// incident. Point m365.Collector.LoginURL and GraphURL at URL().
package fakegraph

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Credentials of the simulated app registration.
const (
	TenantID = "6d1c3b1e-5a7f-4c2e-9b8d-1f2e3d4c5b6a"
	ClientID = "3f9a8b7c-1d2e-4f5a-8b9c-0d1e2f3a4b5c"
	Secret   = "demo~Secret.1234567890"
	token    = "fake-graph-token"
)

// Server is a running simulation.
type Server struct {
	srv *httptest.Server

	mu       sync.Mutex
	denied   map[string]bool // path prefixes answered with 403
	syncAge  time.Duration
	incident bool
}

// New starts the simulation.
func New() *Server {
	s := &Server{denied: map[string]bool{}, syncAge: 18 * time.Minute, incident: true}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// URL is the base URL for both login and Graph.
func (s *Server) URL() string { return s.srv.URL }

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// Deny answers requests below the Graph path prefix (e.g. "/v1.0/applications") with 403.
func (s *Server) Deny(prefix string) {
	s.mu.Lock()
	s.denied[prefix] = true
	s.mu.Unlock()
}

// SetSyncAge sets how long ago Entra Connect last synchronised.
func (s *Server) SetSyncAge(d time.Duration) {
	s.mu.Lock()
	s.syncAge = d
	s.mu.Unlock()
}

// SetIncident switches the Exchange Online incident on or off.
func (s *Server) SetIncident(on bool) {
	s.mu.Lock()
	s.incident = on
	s.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func graphErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token") {
		s.token(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+token {
		graphErr(w, http.StatusUnauthorized, "InvalidAuthenticationToken", "Access token is empty or invalid.")
		return
	}
	s.mu.Lock()
	for p := range s.denied {
		if strings.HasPrefix(r.URL.Path, p) {
			s.mu.Unlock()
			graphErr(w, http.StatusForbidden, "Authorization_RequestDenied", "Insufficient privileges to complete the operation.")
			return
		}
	}
	syncAge, incident := s.syncAge, s.incident
	s.mu.Unlock()
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	day := 24 * time.Hour
	list := func(v ...any) map[string]any { return map[string]any{"value": v} }

	switch r.URL.Path {
	case "/v1.0/organization":
		writeJSON(w, 200, list(map[string]any{
			"id": TenantID, "displayName": "Muster GmbH", "countryLetterCode": "DE",
			"onPremisesSyncEnabled": true, "onPremisesLastSyncDateTime": at(-syncAge), "onPremisesLastPasswordSyncDateTime": at(-2 * time.Minute),
			"verifiedDomains": []map[string]any{{"name": "muster-gmbh.de", "isDefault": true}, {"name": "muster-gmbh.com"}, {"name": "mustergmbh.onmicrosoft.com", "isInitial": true}},
		}))
	case "/v1.0/domains":
		writeJSON(w, 200, list(
			map[string]any{"id": "muster-gmbh.de", "isVerified": true, "isDefault": true},
			map[string]any{"id": "muster-gmbh.com", "isVerified": true},
			map[string]any{"id": "mustergmbh.onmicrosoft.com", "isVerified": true, "isInitial": true},
			map[string]any{"id": "muster-shop.de", "isVerified": false},
		))
	case "/v1.0/subscribedSkus":
		sku := func(id, part, status string, enabled, warning, consumed int) map[string]any {
			return map[string]any{"skuId": id, "skuPartNumber": part, "capabilityStatus": status, "appliesTo": "User", "consumedUnits": consumed,
				"prepaidUnits": map[string]int{"enabled": enabled, "warning": warning, "suspended": 0, "lockedOut": 0}}
		}
		writeJSON(w, 200, list(
			sku("cbdc14ab-d96c-4c30-b9f4-6ada7cdc1d46", "SPB", "Enabled", 25, 0, 23),
			sku("4b9405b0-7788-4568-add1-99614e613b69", "EXCHANGESTANDARD", "Enabled", 5, 0, 5),
			sku("078d2b04-f1bd-4111-bbd4-b4b1b354cef4", "AAD_PREMIUM", "Warning", 0, 3, 3),
			sku("639dec6b-bb19-468b-871c-c5c441c4b0cb", "Microsoft_365_Copilot", "Enabled", 2, 0, 2),
			sku("f30db892-07e9-47e9-837c-80727f46fd3d", "FLOW_FREE", "Enabled", 10000, 0, 4),
		))
	case "/v1.0/directory/subscriptions":
		sub := func(id, status string, trial bool, next time.Duration) map[string]any {
			return map[string]any{"skuId": id, "status": status, "isTrial": trial, "nextLifecycleDateTime": at(next)}
		}
		writeJSON(w, 200, list(
			sub("cbdc14ab-d96c-4c30-b9f4-6ada7cdc1d46", "Enabled", false, 214*day),
			sub("4b9405b0-7788-4568-add1-99614e613b69", "Enabled", false, 214*day),
			sub("078d2b04-f1bd-4111-bbd4-b4b1b354cef4", "Warning", false, 22*day),
			sub("639dec6b-bb19-468b-871c-c5c441c4b0cb", "Enabled", true, 9*day+3*time.Hour),
		))
	case "/v1.0/applications":
		app := func(id, name string, secrets, certs map[string]time.Duration) map[string]any {
			pw, keys := []map[string]string{}, []map[string]string{}
			for n, d := range secrets {
				pw = append(pw, map[string]string{"displayName": n, "endDateTime": at(d)})
			}
			for n, d := range certs {
				keys = append(keys, map[string]string{"displayName": n, "endDateTime": at(d)})
			}
			return map[string]any{"appId": id, "displayName": name, "passwordCredentials": pw, "keyCredentials": keys}
		}
		// two pages, as Graph pages long lists
		if r.URL.Query().Get("page") == "" {
			writeJSON(w, 200, map[string]any{"@odata.nextLink": s.srv.URL + "/v1.0/applications?page=2", "value": []any{
				app(ClientID, "PK95 Monitoring", map[string]time.Duration{"monitoring": 300 * day}, nil),
				app("8c1d2e3f-4a5b-4c6d-8e9f-0a1b2c3d4e5f", "Scan-to-Mail Drucker", map[string]time.Duration{"smtp": 12*day + 5*time.Hour}, nil),
				app("9d2e3f4a-5b6c-4d7e-9f0a-1b2c3d4e5f6a", "Veeam Backup for Microsoft 365", nil, map[string]time.Duration{"CN=VBO365": 143 * day}),
			}})
			return
		}
		writeJSON(w, 200, list(
			app("0e3f4a5b-6c7d-4e8f-0a1b-2c3d4e5f6a7b", "Altes Intranet", map[string]time.Duration{"alt": -400 * day}, nil),
			app("1f4a5b6c-7d8e-4f9a-1b2c-3d4e5f6a7b8c", "Teams-Telefonie Connector", map[string]time.Duration{"2025": -3 * day, "": -40 * day}, nil),
			app("2a5b6c7d-8e9f-4a0b-2c3d-4e5f6a7b8c9d", "Ohne Geheimnis", nil, nil),
		))
	case "/v1.0/admin/serviceAnnouncement/healthOverviews":
		svc := func(id, name, status string) map[string]string {
			return map[string]string{"id": id, "service": name, "status": status}
		}
		exchange := "serviceOperational"
		if incident {
			exchange = "serviceDegradation"
		}
		writeJSON(w, 200, list(
			svc("Exchange", "Exchange Online", exchange), svc("microsoftteams", "Microsoft Teams", "serviceOperational"),
			svc("SharePoint", "SharePoint Online", "serviceOperational"), svc("OneDriveForBusiness", "OneDrive for Business", "serviceOperational"),
			svc("OrgLiveID", "Microsoft Entra", "serviceOperational"), svc("O365Client", "Microsoft 365 Apps", "serviceOperational"),
			svc("Intune", "Microsoft Intune", "serviceOperational"), svc("OSDPPlatform", "Microsoft 365 suite", "serviceOperational"),
		))
	case "/v1.0/admin/serviceAnnouncement/issues":
		issues := []any{map[string]any{"id": "MO918273", "title": "Hinweis: Änderungen an der Kalenderfreigabe", "service": "Microsoft 365 suite",
			"classification": "advisory", "status": "serviceOperational", "startDateTime": at(-3 * day), "isResolved": false}}
		if incident {
			issues = append(issues, map[string]any{"id": "EX982341", "title": "Eingehende E-Mails werden für einige Benutzer verzögert zugestellt",
				"service": "Exchange Online", "classification": "incident", "status": "serviceDegradation", "startDateTime": at(-95 * time.Minute), "isResolved": false})
		}
		writeJSON(w, 200, map[string]any{"value": issues})
	default:
		graphErr(w, http.StatusNotFound, "Request_ResourceNotFound", fmt.Sprintf("Resource %s does not exist.", r.URL.Path))
	}
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.ParseForm() != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	tenant := strings.Split(strings.Trim(r.URL.Path, "/"), "/")[0]
	switch {
	case tenant != TenantID && tenant != "mustergmbh.onmicrosoft.com":
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_tenant", "error_codes": []int{90002},
			"error_description": "AADSTS90002: Tenant '" + tenant + "' not found."})
	case r.PostForm.Get("client_id") != ClientID:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unauthorized_client", "error_codes": []int{700016},
			"error_description": "AADSTS700016: Application not found in the directory."})
	case r.PostForm.Get("client_secret") != Secret:
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid_client", "error_codes": []int{7000215},
			"error_description": "AADSTS7000215: Invalid client secret provided."})
	case r.PostForm.Get("grant_type") != "client_credentials" || !strings.HasSuffix(r.PostForm.Get("scope"), "/.default"):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"token_type": "Bearer", "expires_in": 3599, "access_token": token})
	}
}
