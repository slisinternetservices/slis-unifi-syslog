// Package tlscert provides the main server's TLS certificate (a configured
// one, else a self-signed one generated on first start) and the public key
// pin probes use to trust it.
package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// LoadOrCreate returns the certificate in certFile/keyFile when both are
// set, else the self-signed certificate in dir (created when missing).
func LoadOrCreate(dir, certFile, keyFile string) (tls.Certificate, error) {
	if certFile != "" && keyFile != "" {
		return tls.LoadX509KeyPair(certFile, keyFile)
	}
	cp, kp := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if c, err := tls.LoadX509KeyPair(cp, kp); err == nil {
		return c, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return tls.Certificate{}, err
	}
	certPEM, keyPEM, err := selfSigned()
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(kp, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(cp, certPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}

func selfSigned() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	host, _ := os.Hostname()
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "UniFi Syslog Server " + host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(20, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	if host != "" {
		tpl.DNSNames = append(tpl.DNSNames, host)
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
				tpl.IPAddresses = append(tpl.IPAddresses, n.IP)
			}
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), nil
}

// Pin is the base64 SHA-256 of a certificate's SubjectPublicKeyInfo, the
// format of curl --pinnedpubkey sha256//<pin>.
func Pin(c *x509.Certificate) string {
	h := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(h[:])
}

// LeafPin returns the pin of a loaded certificate.
func LeafPin(c tls.Certificate) string {
	if len(c.Certificate) == 0 {
		return ""
	}
	x, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return ""
	}
	return Pin(x)
}

// ClientConfig trusts a server whose certificate is valid for its name
// under the system roots, or whose public key matches pin (for the
// self-signed certificate). An empty pin only accepts valid certificates.
func ClientConfig(serverName, pin string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Verification happens below, so a pinned self-signed certificate
		// (with any name) is accepted as well.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("server sent no certificate")
			}
			if pin != "" {
				for _, c := range cs.PeerCertificates {
					if Pin(c) == pin {
						return nil
					}
				}
			}
			inter := x509.NewCertPool()
			for _, c := range cs.PeerCertificates[1:] {
				inter.AddCert(c)
			}
			_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{DNSName: serverName, Intermediates: inter})
			if err != nil && pin != "" {
				return errors.New("server certificate does not match the pinned key and is not trusted: " + err.Error())
			}
			return err
		},
	}
}
