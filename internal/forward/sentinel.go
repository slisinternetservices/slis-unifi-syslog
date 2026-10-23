package forward

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
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
	"strings"
	"sync"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

// Microsoft Sentinel through the Azure Monitor Logs Ingestion API: an Entra
// app (client secret or certificate) gets a token for monitor.azure.com and
// posts JSON rows to a data collection rule stream. The legacy HTTP Data
// Collector API is not used; Microsoft is retiring it.

const (
	DefaultSentinelStream = "Custom-UnifiSyslog"
	DefaultCEFStream      = "Custom-UnifiCommonSecurityLog"
	sentinelMaxBody       = 1_000_000 // the API accepts at most 1 MB per call
)

// loginBase is the Entra token endpoint host; tests replace it.
var loginBase = "https://login.microsoftonline.com"

type sentinel struct {
	s    store.ForwardSettings
	hc   *http.Client
	cert *x509.Certificate
	key  *rsa.PrivateKey

	mu      sync.Mutex
	token   string
	expires time.Time
}

func newSentinel(s store.ForwardSettings, hc *http.Client) (*sentinel, error) {
	if s.TenantID == "" || s.ClientID == "" {
		return nil, errors.New("tenant ID and client ID are required")
	}
	if err := needURL(s.Endpoint); err != nil {
		return nil, errors.New("enter the logs ingestion endpoint, e.g. https://name.westeurope-1.ingest.monitor.azure.com")
	}
	if !strings.HasPrefix(s.DCRID, "dcr-") {
		return nil, errors.New("enter the immutable ID of the data collection rule (dcr-…)")
	}
	sn := &sentinel{s: s, hc: hc}
	if s.AuthMethod == "certificate" {
		cert, key, err := parseCertKey(s.Certificate)
		if err != nil {
			return nil, err
		}
		sn.cert, sn.key = cert, key
	} else if s.ClientSecret == "" {
		return nil, errors.New("a client secret is required")
	}
	return sn, nil
}

// parseCertKey reads a PEM certificate and its RSA private key.
func parseCertKey(pemData string) (*x509.Certificate, *rsa.PrivateKey, error) {
	var cert *x509.Certificate
	var key *rsa.PrivateKey
	rest := []byte(pemData)
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		switch b.Type {
		case "CERTIFICATE":
			if cert == nil {
				c, err := x509.ParseCertificate(b.Bytes)
				if err != nil {
					return nil, nil, fmt.Errorf("certificate: %w", err)
				}
				cert = c
			}
		case "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
			if err != nil {
				return nil, nil, fmt.Errorf("private key: %w", err)
			}
			rk, ok := k.(*rsa.PrivateKey)
			if !ok {
				return nil, nil, errors.New("the private key must be an RSA key")
			}
			key = rk
		case "RSA PRIVATE KEY":
			k, err := x509.ParsePKCS1PrivateKey(b.Bytes)
			if err != nil {
				return nil, nil, fmt.Errorf("private key: %w", err)
			}
			key = k
		}
	}
	if cert == nil || key == nil {
		return nil, nil, errors.New("paste the certificate and its unencrypted private key in PEM format")
	}
	return cert, key, nil
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// assertion is the signed client assertion for certificate sign-in.
func (sn *sentinel) assertion(aud string) (string, error) {
	thumb := sha1.Sum(sn.cert.Raw)
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "x5t": b64url(thumb[:])})
	now := time.Now()
	claims, _ := json.Marshal(map[string]any{"aud": aud, "iss": sn.s.ClientID, "sub": sn.s.ClientID, "jti": newID(),
		"nbf": now.Add(-time.Minute).Unix(), "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()})
	signing := b64url(hdr) + "." + b64url(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(nil, sn.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + b64url(sig), nil
}

func (sn *sentinel) accessToken(ctx context.Context) (string, error) {
	sn.mu.Lock()
	defer sn.mu.Unlock()
	if sn.token != "" && time.Until(sn.expires) > 2*time.Minute {
		return sn.token, nil
	}
	tokenURL := loginBase + "/" + url.PathEscape(sn.s.TenantID) + "/oauth2/v2.0/token"
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {sn.s.ClientID}, "scope": {"https://monitor.azure.com//.default"}}
	if sn.key != nil {
		a, err := sn.assertion(tokenURL)
		if err != nil {
			return "", err
		}
		form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
		form.Set("client_assertion", a)
	} else {
		form.Set("client_secret", sn.s.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := sn.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		desc, _, _ := strings.Cut(out.Description, "\r\n")
		desc, _, _ = strings.Cut(desc, " Trace ID:")
		return "", fmt.Errorf("Entra sign-in failed: %s %s", out.Error, desc)
	}
	sn.token = out.AccessToken
	sn.expires = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	return sn.token, nil
}

func (sn *sentinel) Limits() (int, int) { return 5000, 800 << 10 }
func (sn *sentinel) Close()             {}

func (sn *sentinel) Send(ctx context.Context, recs []Record) (int, error) {
	var custom, csl []map[string]any
	for i := range recs {
		if sn.s.TableMode == "split" && toCSL(&recs[i]) {
			csl = append(csl, compact(cslRow(&recs[i])))
		} else {
			custom = append(custom, compact(sentinelRow(&recs[i])))
		}
	}
	if len(custom) > 0 {
		if err := sn.post(ctx, first(sn.s.Stream, DefaultSentinelStream), custom); err != nil {
			return 0, err
		}
	}
	if len(csl) > 0 {
		if err := sn.post(ctx, first(sn.s.CEFStream, DefaultCEFStream), csl); err != nil {
			return 0, err
		}
	}
	return 0, nil
}

// compact leaves out empty strings; missing columns are simply empty in
// Log Analytics and the rows get much smaller (cheaper).
func compact(row map[string]any) map[string]any {
	for k, v := range row {
		if s, ok := v.(string); ok && s == "" {
			delete(row, k)
		}
	}
	return row
}

func (sn *sentinel) post(ctx context.Context, streamName string, rows []map[string]any) error {
	body, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	if len(body) > sentinelMaxBody {
		return errTooLarge
	}
	tok, err := sn.accessToken(ctx)
	if err != nil {
		return err
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(body)
	zw.Close()
	u := strings.TrimRight(sn.s.Endpoint, "/") + "/dataCollectionRules/" + url.PathEscape(sn.s.DCRID) + "/streams/" + url.PathEscape(streamName) + "?api-version=2023-01-01"
	resp, err := post(ctx, sn.hc, u, gz.Bytes(), http.Header{"Authorization": {"Bearer " + tok}, "Content-Type": {"application/json"}, "Content-Encoding": {"gzip"}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		sn.mu.Lock()
		sn.token = ""
		sn.mu.Unlock()
	}
	if resp.StatusCode >= 300 {
		return httpError(resp)
	}
	return nil
}
