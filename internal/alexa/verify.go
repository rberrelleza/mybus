package alexa

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

// Verification of requests sent to a skill hosted as a web service, as
// required by Amazon:
// https://developer.amazon.com/en-US/docs/alexa/custom-skills/host-a-custom-skill-as-a-web-service.html
// (Not needed on AWS Lambda, where the Alexa trigger authenticates calls.)

const (
	certHost         = "s3.amazonaws.com"
	certPathPrefix   = "/echo.api/"
	certSAN          = "echo-api.amazon.com"
	maxTimestampSkew = 150 * time.Second
)

// Verifier checks the signature and timestamp of incoming Alexa requests.
type Verifier struct {
	// Roots overrides the system root CAs (used in tests).
	Roots *x509.CertPool
	// Client fetches the signing certificate chain.
	Client *http.Client
	// Now overrides the clock (used in tests).
	Now func() time.Time

	mu    sync.Mutex
	certs map[string]*x509.Certificate
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// Verify validates the request signature and timestamp. body is the raw
// request body.
func (v *Verifier) Verify(header http.Header, body []byte) error {
	certURL := header.Get("SignatureCertChainUrl")
	signature := header.Get("Signature-256")
	if certURL == "" || signature == "" {
		return errors.New("missing signature headers")
	}
	if err := validateCertURL(certURL); err != nil {
		return err
	}

	cert, err := v.certificate(certURL)
	if err != nil {
		return err
	}

	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("invalid signature encoding: %w", err)
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return errors.New("signing certificate does not have an RSA key")
	}
	digest := sha256.Sum256(body)
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return fmt.Errorf("invalid request signature: %w", err)
	}

	var envelope struct {
		Request struct {
			Timestamp string `json:"timestamp"`
		} `json:"request"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	ts, err := time.Parse(time.RFC3339, envelope.Request.Timestamp)
	if err != nil {
		return fmt.Errorf("invalid request timestamp: %w", err)
	}
	if skew := v.now().Sub(ts); skew > maxTimestampSkew || skew < -maxTimestampSkew {
		return fmt.Errorf("request timestamp %s is too far from the current time", ts)
	}
	return nil
}

func validateCertURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid certificate url: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("certificate url must use https: %s", raw)
	}
	if !strings.EqualFold(u.Hostname(), certHost) {
		return fmt.Errorf("certificate url has invalid host: %s", raw)
	}
	if p := u.Port(); p != "" && p != "443" {
		return fmt.Errorf("certificate url has invalid port: %s", raw)
	}
	if !strings.HasPrefix(path.Clean(u.Path), certPathPrefix) {
		return fmt.Errorf("certificate url has invalid path: %s", raw)
	}
	return nil
}

func (v *Verifier) certificate(certURL string) (*x509.Certificate, error) {
	v.mu.Lock()
	cached := v.certs[certURL]
	v.mu.Unlock()
	if cached != nil && v.now().Before(cached.NotAfter) {
		return cached, nil
	}

	client := v.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	resp, err := client.Get(certURL)
	if err != nil {
		return nil, fmt.Errorf("fetching certificate chain: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching certificate chain: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("reading certificate chain: %w", err)
	}

	cert, err := v.verifyChain(data)
	if err != nil {
		return nil, err
	}

	v.mu.Lock()
	if v.certs == nil {
		v.certs = map[string]*x509.Certificate{}
	}
	v.certs[certURL] = cert
	v.mu.Unlock()
	return cert, nil
}

// verifyChain parses a PEM chain (leaf first), and checks it chains to a
// trusted root, is currently valid and is issued for echo-api.amazon.com.
func (v *Verifier) verifyChain(data []byte) (*x509.Certificate, error) {
	var certs []*x509.Certificate
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parsing certificate: %w", err)
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		return nil, errors.New("certificate chain is empty")
	}

	intermediates := x509.NewCertPool()
	for _, c := range certs[1:] {
		intermediates.AddCert(c)
	}
	leaf := certs[0]
	if _, err := leaf.Verify(x509.VerifyOptions{
		DNSName:       certSAN,
		Roots:         v.Roots,
		Intermediates: intermediates,
		CurrentTime:   v.now(),
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return nil, fmt.Errorf("invalid signing certificate: %w", err)
	}
	return leaf, nil
}
