package alexa

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 17, 18, 0, 0, 0, time.UTC)

type pki struct {
	roots *x509.CertPool
	chain []byte
	key   *rsa.PrivateKey
}

func newPKI(t *testing.T, san string) *pki {
	t.Helper()
	caKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Root"},
		NotBefore:             testNow.Add(-time.Hour),
		NotAfter:              testNow.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)

	leafKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: san},
		DNSNames:     []string{san},
		NotBefore:    testNow.Add(-time.Hour),
		NotAfter:     testNow.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	roots := x509.NewCertPool()
	roots.AddCert(ca)
	var chain bytes.Buffer
	_ = pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	_ = pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	return &pki{roots: roots, chain: chain.Bytes(), key: leafKey}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (p *pki) verifier() *Verifier {
	return &Verifier{
		Roots: p.roots,
		Now:   func() time.Time { return testNow },
		Client: &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(p.chain))}, nil
		})},
	}
}

func (p *pki) sign(body []byte) string {
	digest := sha256.Sum256(body)
	sig, _ := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, digest[:])
	return base64.StdEncoding.EncodeToString(sig)
}

const goodURL = "https://s3.amazonaws.com/echo.api/echo-api-cert.pem"

func body(ts time.Time) []byte {
	return []byte(`{"version":"1.0","request":{"type":"LaunchRequest","timestamp":"` + ts.Format(time.RFC3339) + `"}}`)
}

func headers(url, sig string) http.Header {
	h := http.Header{}
	h.Set("SignatureCertChainUrl", url)
	h.Set("Signature-256", sig)
	return h
}

func TestVerifyValid(t *testing.T) {
	p := newPKI(t, "echo-api.amazon.com")
	b := body(testNow)
	if err := p.verifier().Verify(headers(goodURL, p.sign(b)), b); err != nil {
		t.Fatalf("expected valid request, got %v", err)
	}
}

func TestVerifyRejects(t *testing.T) {
	p := newPKI(t, "echo-api.amazon.com")
	b := body(testNow)
	sig := p.sign(b)

	cases := map[string]struct {
		h    http.Header
		body []byte
	}{
		"missing headers":   {http.Header{}, b},
		"tampered body":     {headers(goodURL, sig), bytes.Replace(b, []byte("Launch"), []byte("Intent"), 1)},
		"stale timestamp":   {headers(goodURL, p.sign(body(testNow.Add(-3*time.Minute)))), body(testNow.Add(-3 * time.Minute))},
		"http url":          {headers("http://s3.amazonaws.com/echo.api/echo-api-cert.pem", sig), b},
		"wrong host":        {headers("https://notamazon.com/echo.api/echo-api-cert.pem", sig), b},
		"wrong path":        {headers("https://s3.amazonaws.com/EcHo.aPi/echo-api-cert.pem", sig), b},
		"path traversal":    {headers("https://s3.amazonaws.com/echo.api/../invalid.pem", sig), b},
		"wrong port":        {headers("https://s3.amazonaws.com:563/echo.api/echo-api-cert.pem", sig), b},
		"garbage signature": {headers(goodURL, "bm90IGEgc2lnbmF0dXJl"), b},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := p.verifier().Verify(c.h, c.body); err == nil {
				t.Fatal("expected request to be rejected")
			}
		})
	}
}

func TestVerifyAcceptsURLVariants(t *testing.T) {
	p := newPKI(t, "echo-api.amazon.com")
	b := body(testNow)
	for _, u := range []string{
		"https://s3.amazonaws.com:443/echo.api/echo-api-cert.pem",
		"HTTPS://s3.AmazonAWS.com/echo.api/echo-api-cert.pem",
		"https://s3.amazonaws.com/echo.api/../echo.api/echo-api-cert.pem",
	} {
		if err := p.verifier().Verify(headers(u, p.sign(b)), b); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
}

func TestVerifyRejectsWrongCertificate(t *testing.T) {
	p := newPKI(t, "not-echo-api.example.com")
	b := body(testNow)
	err := p.verifier().Verify(headers(goodURL, p.sign(b)), b)
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected certificate error, got %v", err)
	}

	untrusted := newPKI(t, "echo-api.amazon.com")
	v := untrusted.verifier()
	v.Roots = p.roots // different CA
	if err := v.Verify(headers(goodURL, untrusted.sign(b)), b); err == nil {
		t.Fatal("expected untrusted chain to be rejected")
	}
}
