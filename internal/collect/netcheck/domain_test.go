package netcheck

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pk95monitoring/internal/model"
)

// fakeRDAP serves an IANA bootstrap file and domain records for .com, .de and .co.uk.
func fakeRDAP(t *testing.T) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	exp := time.Now().Add(20*24*time.Hour + time.Hour).UTC().Format(time.RFC3339)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/rdap+json")
		switch r.URL.Path {
		case "/dns.json":
			fmt.Fprintf(w, `{"services":[[["com","net"],["%[1]s/com/"]],[["de"],["%[1]s/de/"]],[["co.uk"],["%[1]s/couk/"]]]}`, srv.URL)
		case "/com/domain/example.com":
			fmt.Fprintf(w, `{"status":["client transfer prohibited"],"events":[{"eventAction":"registration","eventDate":"2001-05-02T10:00:00Z"},
				{"eventAction":"expiration","eventDate":%q}],"entities":[{"roles":["registrar"],"vcardArray":["vcard",[["version",{},"text","4.0"],["fn",{},"text","Beispiel Registrar GmbH"]]]}]}`, exp)
		case "/com/domain/held.com":
			fmt.Fprint(w, `{"status":["client hold"],"events":[{"eventAction":"expiration","eventDate":"2099-01-01T00:00:00Z"}]}`)
		case "/de/domain/beispiel.de":
			fmt.Fprint(w, `{"status":["active"],"events":[{"eventAction":"last changed","eventDate":"2024-01-01T00:00:00Z"}]}`)
		case "/couk/domain/shop.co.uk":
			fmt.Fprint(w, `{"events":[{"eventAction":"expiration","eventDate":"2000-01-01T00:00:00Z"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := rdapBootstrapURL
	SetRDAPBootstrap(srv.URL + "/dns.json")
	t.Cleanup(func() { SetRDAPBootstrap(old) })
	return &hits
}

func TestDomainName(t *testing.T) {
	for in, want := range map[string]string{
		"www.Example.com.": "example.com", "https://shop.example.co.uk/x": "example.co.uk", "beispiel.de": "beispiel.de",
	} {
		if got, err := domainName(in, ""); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	if got, _ := domainName("", "mail.example.org"); got != "example.org" {
		t.Errorf("device host: %q", got)
	}
	for _, bad := range []string{"10.0.0.1", "localhost", "com", ""} {
		if _, err := domainName(bad, ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDomainCheck(t *testing.T) {
	hits := fakeRDAP(t)
	run := func(target string) model.Check {
		return Run(context.Background(), model.NetCheck{ID: "d1", Type: model.NetDomain, Target: target}, "")
	}

	c := run("www.example.com")
	if c.State != model.StateOK || len(c.Metrics) != 1 || c.Metrics[0].Name != "domain.days" || c.Metrics[0].Value != 20 {
		t.Fatalf("example.com: %+v", c)
	}
	if c.Name != "Domain example.com" || c.Facts["Registrar"] != "Beispiel Registrar GmbH" || c.Facts["Registriert seit"] != "02.05.2001" ||
		!strings.HasPrefix(c.Message, "Registriert bis ") {
		t.Fatalf("example.com facts: %+v", c)
	}
	n := hits.Load()
	run("example.com") // cached
	if hits.Load() != n {
		t.Fatal("not cached")
	}

	if c := run("beispiel.de"); c.State != model.StateOK || len(c.Metrics) != 0 || !strings.Contains(c.Message, "kein Ablaufdatum") {
		t.Fatalf(".de: %+v", c)
	}
	if c := run("frei.com"); c.State != model.StateCrit || c.Message != "Domain ist nicht registriert" {
		t.Fatalf("not registered: %+v", c)
	}
	if c := run("held.com"); c.State != model.StateCrit || !strings.Contains(c.Message, "gesperrt") {
		t.Fatalf("client hold: %+v", c)
	}
	if c := run("shop.co.uk"); c.State != model.StateCrit || !strings.HasPrefix(c.Message, "Abgelaufen am") {
		t.Fatalf("expired: %+v", c)
	}
	if c := run("example.xyz"); c.State != model.StateUnknown || !strings.Contains(c.Message, "keinen RDAP-Dienst") {
		t.Fatalf("no RDAP: %+v", c)
	}
	if errs := Validate(model.NetCheck{Type: model.NetDomain, Target: "10.0.0.1"}, ""); errs["target"] == "" {
		t.Fatal("IP accepted as domain")
	}
}
