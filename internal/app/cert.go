package app

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pk95monitoring/internal/agentdist"
)

// loadOrCreateCert returns the HTTPS certificate: a custom one (cert.pem/key.pem in the data
// directory) or a self-signed one created on first start. The self-signed certificate is
// never replaced automatically: agents pin its fingerprint.
func loadOrCreateCert(dataDir, extraHost string) (tls.Certificate, error) {
	certPath, keyPath := filepath.Join(dataDir, "cert.pem"), filepath.Join(dataDir, "key.pem")
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return c, nil
	} else if _, statErr := os.Stat(certPath); statErr == nil {
		return tls.Certificate{}, err // a broken custom certificate must not be overwritten
	}
	certPEM, keyPEM, err := selfSigned(extraHost, time.Now())
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}

// selfSigned creates a P-256 certificate valid for 10 years for the host name, localhost,
// the local IP addresses and extraHost (the host of PK95MON_PUBLIC_URL).
func selfSigned(extraHost string, now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	host, _ := os.Hostname()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 126))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: firstNonEmpty(extraHost, host, "pk95monitoring"), Organization: []string{"PK95 Monitoring"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	addSAN(tmpl, host)
	addSAN(tmpl, "localhost")
	addSAN(tmpl, extraHost)
	for _, ip := range localIPs() {
		addSAN(tmpl, ip.String())
	}
	addSAN(tmpl, "127.0.0.1")
	addSAN(tmpl, "::1")
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// addSAN adds a host name or IP address once.
func addSAN(c *x509.Certificate, h string) {
	h = strings.Trim(strings.TrimSpace(h), "[]")
	if h == "" {
		return
	}
	if ip := net.ParseIP(h); ip != nil {
		for _, x := range c.IPAddresses {
			if x.Equal(ip) {
				return
			}
		}
		c.IPAddresses = append(c.IPAddresses, ip)
		return
	}
	h = strings.ToLower(h)
	for _, x := range c.DNSNames {
		if x == h {
			return
		}
	}
	c.DNSNames = append(c.DNSNames, h)
}

// localIPs returns the global unicast addresses of the interfaces that are up.
func localIPs() []net.IP {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.IsGlobalUnicast() {
				out = append(out, n.IP)
			}
		}
	}
	return out
}

// publicHost returns the host of PK95MON_PUBLIC_URL ("" when unset or invalid).
func publicHost(publicURL string) string {
	if publicURL == "" {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(publicURL))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

type tlsInfo struct {
	fingerprint string // SHA-256 of the leaf certificate, AA:BB:…
	pin         string // base64 SHA-256 of the public key (curl --pinnedpubkey)
	trusted     bool   // the chain verifies against the system CAs
}

// certInfo describes the leaf certificate. Agents and the bootstrap commands pin it unless
// it is trusted by the system CAs (custom certificate from a public or internal CA that the
// clients know).
func certInfo(c tls.Certificate) tlsInfo {
	leaf := c.Leaf
	if leaf == nil && len(c.Certificate) > 0 {
		leaf, _ = x509.ParseCertificate(c.Certificate[0])
	}
	if leaf == nil {
		return tlsInfo{}
	}
	sum := sha256.Sum256(leaf.Raw)
	info := tlsInfo{fingerprint: formatFingerprint(sum[:]), pin: agentdist.PinFromCert(leaf)}
	inter := x509.NewCertPool()
	for _, der := range c.Certificate[1:] {
		if x, err := x509.ParseCertificate(der); err == nil {
			inter.AddCert(x)
		}
	}
	_, err := leaf.Verify(x509.VerifyOptions{Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	info.trusted = err == nil
	return info
}

// formatFingerprint renders a hash as AA:BB:… (the form browsers and Proxmox show).
func formatFingerprint(b []byte) string {
	h := strings.ToUpper(hex.EncodeToString(b))
	parts := make([]string, 0, len(h)/2)
	for i := 0; i+1 < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}
