// Package certs generates and installs the loopback TLS credentials the
// browser needs to reach the bridge.
//
// The client library hardcodes https://127.51.68.120:8181, so the certificate
// must carry subjectAltName = IP:127.51.68.120 — which no public CA will ever
// issue. A locally installed trust anchor is the only option.
//
// Two rules, learned the hard way (see docs/04-certificates-and-browser-trust.md):
//
//   - Gecko rejects a self-issued CA:TRUE certificate used as its own
//     end-entity. A genuine CA -> leaf chain is required, not one self-signed
//     certificate.
//   - The private key is generated per user, on the machine, mode 0600, and
//     never leaves it. Shipping a CA key inside a package would let anyone who
//     downloads it forge certificates for any site.
package certs

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// LoopbackIP is hardcoded in 3dconnexion.js:103 and cannot be configured.
var LoopbackIP = net.IPv4(127, 51, 68, 120)

// Nickname identifies our CA inside browser trust stores.
const Nickname = "SpaceMouse Bridge Local CA"

const (
	caValidity   = 10 * 365 * 24 * time.Hour
	leafValidity = 825 * 24 * time.Hour
	// renewBefore is how much life must remain before the leaf is reissued.
	renewBefore = 30 * 24 * time.Hour
)

// Paths locates the credential files.
type Paths struct {
	Dir       string
	CACert    string
	CAKey     string
	LeafCert  string
	LeafKey   string
	FullChain string
}

// DefaultDir is $XDG_DATA_HOME/spacemouse-bridge, or ~/.local/share/... .
func DefaultDir() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "spacemouse-bridge"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "spacemouse-bridge"), nil
}

// At builds the file layout under dir.
func At(dir string) Paths {
	return Paths{
		Dir:       dir,
		CACert:    filepath.Join(dir, "ca.pem"),
		CAKey:     filepath.Join(dir, "ca-key.pem"),
		LeafCert:  filepath.Join(dir, "leaf.pem"),
		LeafKey:   filepath.Join(dir, "leaf-key.pem"),
		FullChain: filepath.Join(dir, "fullchain.pem"),
	}
}

// Status reports what exists and how much life is left.
type Status struct {
	CAExists     bool
	LeafExists   bool
	CAExpiry     time.Time
	LeafExpiry   bool // true when the leaf is present and still valid
	LeafNotAfter time.Time
	KeyModeOK    bool
}

// Ensure creates or renews the credentials as needed and reports whether
// anything changed.
func Ensure(p Paths) (changed bool, err error) {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return false, fmt.Errorf("creating %s: %w", p.Dir, err)
	}
	// Tighten an existing directory that was created too permissively.
	if err := os.Chmod(p.Dir, 0o700); err != nil {
		return false, fmt.Errorf("securing %s: %w", p.Dir, err)
	}

	caCert, caKey, err := loadCA(p)
	if err != nil {
		if caCert, caKey, err = createCA(p); err != nil {
			return false, err
		}
		changed = true
	}

	if leafOK(p, caCert) {
		return changed, nil
	}
	if err := createLeaf(p, caCert, caKey); err != nil {
		return changed, err
	}
	return true, nil
}

func loadCA(p Paths) (*x509.Certificate, *rsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(p.CACert)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(p.CAKey)
	if err != nil {
		return nil, nil, err
	}
	cert, err := parseCert(certPEM)
	if err != nil {
		return nil, nil, err
	}
	if time.Now().After(cert.NotAfter) {
		return nil, nil, fmt.Errorf("CA expired on %s", cert.NotAfter)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, nil, fmt.Errorf("CA key is not PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("CA key is not RSA")
	}
	return cert, rsaKey, nil
}

func createCA(p Paths) (*x509.Certificate, *rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("generating CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: Nickname},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(caValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("signing CA: %w", err)
	}
	if err := writePEM(p.CACert, "CERTIFICATE", der, 0o644); err != nil {
		return nil, nil, err
	}
	if err := writeKey(p.CAKey, key); err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

// leafOK reports whether the existing leaf is usable: present, signed by this
// CA, valid for the loopback IP, and not close to expiry.
func leafOK(p Paths, ca *x509.Certificate) bool {
	certPEM, err := os.ReadFile(p.LeafCert)
	if err != nil {
		return false
	}
	if _, err := os.Stat(p.LeafKey); err != nil {
		return false
	}
	cert, err := parseCert(certPEM)
	if err != nil {
		return false
	}
	if time.Now().Add(renewBefore).After(cert.NotAfter) {
		return false
	}
	if err := cert.CheckSignatureFrom(ca); err != nil {
		return false
	}
	for _, ip := range cert.IPAddresses {
		if ip.Equal(LoopbackIP) {
			return true
		}
	}
	return false
}

func createLeaf(p Paths, ca *x509.Certificate, caKey *rsa.PrivateKey) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("generating leaf key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: LoopbackIP.String()},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(leafValidity),
		IsCA:                  false,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{LoopbackIP},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("signing leaf: %w", err)
	}
	if err := writePEM(p.LeafCert, "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	if err := writeKey(p.LeafKey, key); err != nil {
		return err
	}

	// The server presents leaf then CA, so a client that trusts the CA can
	// build the chain without holding a copy of it.
	caPEM, err := os.ReadFile(p.CACert)
	if err != nil {
		return err
	}
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return os.WriteFile(p.FullChain, append(leafPEM, caPEM...), 0o644)
}

func parseCert(p []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(p)
	if block == nil {
		return nil, fmt.Errorf("not PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generating serial: %w", err)
	}
	return n, nil
}

func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	b := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	return os.WriteFile(path, b, mode)
}

// writeKey writes a private key at 0600. Anything looser would let another
// local account impersonate the trust anchor we just installed.
func writeKey(path string, key *rsa.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	return writePEM(path, "PRIVATE KEY", der, 0o600)
}

// Inspect reports the current state of the credentials, for selftest.
func Inspect(p Paths) Status {
	var s Status
	if certPEM, err := os.ReadFile(p.CACert); err == nil {
		if c, err := parseCert(certPEM); err == nil {
			s.CAExists = true
			s.CAExpiry = c.NotAfter
		}
	}
	if certPEM, err := os.ReadFile(p.LeafCert); err == nil {
		if c, err := parseCert(certPEM); err == nil {
			s.LeafExists = true
			s.LeafNotAfter = c.NotAfter
			s.LeafExpiry = time.Now().Before(c.NotAfter)
		}
	}
	s.KeyModeOK = true
	for _, k := range []string{p.CAKey, p.LeafKey} {
		fi, err := os.Stat(k)
		if err != nil || fi.Mode().Perm() != 0o600 {
			s.KeyModeOK = false
		}
	}
	return s
}
