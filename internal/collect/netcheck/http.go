package netcheck

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pk95monitoring/internal/model"
)

const (
	maxRedirects = 5
	keywordLimit = 1 << 20  // the keyword must appear in the first 1 MiB
	drainLimit   = 32 << 20 // read at most this much to measure the full response
)

// rootCAs verifies server certificates; nil = system roots. Replaced in tests.
var rootCAs *x509.CertPool

// dialer opens TCP connections for the http, tcp and tls checks.
var dialer = &net.Dialer{KeepAlive: -1}

// runHTTP requests the URL and checks status code, keyword and certificate.
func runHTTP(ctx context.Context, res *model.Check, c model.NetCheck, timeout time.Duration) {
	method := strings.ToUpper(strings.TrimSpace(c.Method))
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSpace(c.Target), nil)
	if err != nil {
		res.Message = "Ungültige URL: " + err.Error()
		return
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")

	tr := &http.Transport{
		DialContext:         dialer.DialContext,
		TLSClientConfig:     &tls.Config{RootCAs: rootCAs, InsecureSkipVerify: c.IgnoreTLS},
		TLSHandshakeTimeout: timeout,
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return fmt.Errorf("mehr als %d Weiterleitungen", maxRedirects)
			}
			return nil
		},
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		res.State = model.StateCrit
		var verr *tls.CertificateVerificationError
		if errors.As(err, &verr) {
			res.Message = "Zertifikat ungültig: " + certErrText(err)
			if len(verr.UnverifiedCertificates) > 0 {
				addCert(res, verr.UnverifiedCertificates[0])
			}
			return
		}
		res.Message = errText(err, timeout)
		return
	}
	defer resp.Body.Close()

	var head []byte
	if c.Keyword != "" {
		head, err = io.ReadAll(io.LimitReader(resp.Body, keywordLimit))
	}
	if err == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, drainLimit))
	}
	elapsed := time.Since(start)
	if err != nil {
		res.State, res.Message = model.StateCrit, "Fehler beim Lesen der Antwort: "+errText(err, timeout)
		return
	}

	res.Metrics = append(res.Metrics,
		model.Metric{Name: "http.time", Value: ms(elapsed), Unit: "ms"},
		model.Metric{Name: "http.status", Value: float64(resp.StatusCode)})
	res.Facts = map[string]string{"Statuscode": strconv.Itoa(resp.StatusCode)}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		addCert(res, resp.TLS.PeerCertificates[0])
	}

	status := fmt.Sprintf("Status %d", resp.StatusCode)
	switch {
	case c.ExpectStatus != 0 && resp.StatusCode != c.ExpectStatus:
		res.State, res.Message = model.StateCrit, fmt.Sprintf("%s (erwartet %d)", status, c.ExpectStatus)
	case c.ExpectStatus == 0 && (resp.StatusCode < 200 || resp.StatusCode > 399):
		res.State, res.Message = model.StateCrit, status+" "+http.StatusText(resp.StatusCode)
	case c.Keyword != "" && !bytes.Contains(head, []byte(c.Keyword)):
		res.State, res.Message = model.StateCrit, fmt.Sprintf("Suchbegriff „%s“ nicht gefunden (%s)", c.Keyword, status)
	default:
		res.State, res.Message = model.StateOK, fmt.Sprintf("%s in %s", status, fmtMs(elapsed))
	}
}

// addCert adds the cert.days metric and the certificate facts of a leaf certificate.
func addCert(res *model.Check, leaf *x509.Certificate) {
	res.Metrics = append(res.Metrics, model.Metric{Name: "cert.days", Value: certDays(leaf, time.Now()), Unit: "d"})
	if res.Facts == nil {
		res.Facts = map[string]string{}
	}
	res.Facts["Zertifikat gültig bis"] = leaf.NotAfter.Local().Format("02.01.2006 15:04")
	res.Facts["Aussteller"] = issuerName(leaf)
}

// certDays is the number of whole days until the certificate expires (negative = expired).
func certDays(leaf *x509.Certificate, now time.Time) float64 {
	return math.Floor(leaf.NotAfter.Sub(now).Hours() / 24)
}

// issuerName is the common name (or organization) of the certificate issuer.
func issuerName(c *x509.Certificate) string {
	switch {
	case c.Issuer.CommonName != "" && len(c.Issuer.Organization) > 0 && !strings.Contains(c.Issuer.CommonName, c.Issuer.Organization[0]):
		return c.Issuer.CommonName + " (" + c.Issuer.Organization[0] + ")"
	case c.Issuer.CommonName != "":
		return c.Issuer.CommonName
	case len(c.Issuer.Organization) > 0:
		return c.Issuer.Organization[0]
	}
	return c.Issuer.String()
}

// certNames lists the DNS names and IP addresses a certificate is valid for.
func certNames(c *x509.Certificate) string {
	names := append([]string(nil), c.DNSNames...)
	for _, ip := range c.IPAddresses {
		names = append(names, ip.String())
	}
	if len(names) == 0 && c.Subject.CommonName != "" {
		names = append(names, c.Subject.CommonName)
	}
	return strings.Join(names, ", ")
}

// certErrText translates certificate verification errors into German.
func certErrText(err error) string {
	var (
		unknown   x509.UnknownAuthorityError
		hostname  x509.HostnameError
		invalid   x509.CertificateInvalidError
		verifyErr *tls.CertificateVerificationError
	)
	switch {
	case errors.As(err, &unknown):
		return "unbekannte Zertifizierungsstelle (selbst signiert?)"
	case errors.As(err, &hostname):
		return "ausgestellt für " + certNames(hostname.Certificate) + ", nicht für " + hostname.Host
	case errors.As(err, &invalid) && invalid.Reason == x509.Expired:
		if time.Now().After(invalid.Cert.NotAfter) {
			return "abgelaufen am " + invalid.Cert.NotAfter.Local().Format("02.01.2006")
		}
		return "noch nicht gültig (ab " + invalid.Cert.NotBefore.Local().Format("02.01.2006") + ")"
	case errors.As(err, &invalid):
		return invalid.Error()
	case errors.As(err, &verifyErr):
		return verifyErr.Err.Error()
	}
	return err.Error()
}
