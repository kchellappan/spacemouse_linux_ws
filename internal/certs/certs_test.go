package certs

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func ensureIn(t *testing.T, dir string) Paths {
	t.Helper()
	p := At(dir)
	if _, err := Ensure(p); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return p
}

func readCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	c, err := parseCert(b)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return c
}

// The whole point of the chain is that a browser accepts it for the hardcoded
// loopback address. Verify that directly rather than trusting the templates.
func TestEnsureProducesAChainValidForTheLoopbackIP(t *testing.T) {
	p := ensureIn(t, t.TempDir())

	ca := readCert(t, p.CACert)
	leaf := readCert(t, p.LeafCert)

	if !ca.IsCA {
		t.Error("CA certificate does not have IsCA set")
	}
	if leaf.IsCA {
		t.Error("leaf has IsCA set; Gecko rejects a CA used as its own end-entity")
	}

	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("leaf does not chain to the CA: %v", err)
	}
	if err := leaf.VerifyHostname(LoopbackIP.String()); err != nil {
		t.Fatalf("leaf is not valid for %s: %v", LoopbackIP, err)
	}
}

// The server sends leaf then CA so a client holding only the CA can build the
// chain. A fullchain with one certificate would fail only in the browser.
func TestFullChainHasLeafThenCA(t *testing.T) {
	p := ensureIn(t, t.TempDir())

	rest, err := os.ReadFile(p.FullChain)
	if err != nil {
		t.Fatal(err)
	}
	var got []*x509.Certificate
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		got = append(got, c)
	}
	if len(got) != 2 {
		t.Fatalf("fullchain has %d certificates, want 2", len(got))
	}
	if got[0].IsCA {
		t.Error("first certificate in the chain is the CA; the leaf must come first")
	}
	if !got[1].IsCA {
		t.Error("second certificate in the chain is not the CA")
	}
}

// A key another local account can read defeats the point of installing this CA
// into the browser, so the mode is part of the contract.
func TestPrivateKeysAreOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	p := ensureIn(t, dir)

	for _, k := range []string{p.CAKey, p.LeafKey} {
		fi, err := os.Stat(k)
		if err != nil {
			t.Fatalf("stat %s: %v", k, err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s is mode %o, want 600", filepath.Base(k), got)
		}
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("credential directory is mode %o, want 700", got)
	}
}

// Ensure runs on every service start, so a second call must be a no-op rather
// than a fresh CA that invalidates what is already in the browser.
func TestEnsureIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	p := At(dir)

	changed, err := Ensure(p)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first Ensure reported no change")
	}
	before := readCert(t, p.CACert).Raw

	changed, err = Ensure(p)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second Ensure regenerated credentials that were already valid")
	}
	if string(readCert(t, p.CACert).Raw) != string(before) {
		t.Error("the CA changed across calls")
	}
}

// A leaf left behind by a previous CA cannot be served: the browser trusts the
// new CA and the old leaf chains to nothing.
func TestEnsureReissuesTheLeafWhenTheCAIsReplaced(t *testing.T) {
	dir := t.TempDir()
	p := ensureIn(t, dir)
	oldLeaf := readCert(t, p.LeafCert).Raw

	if err := os.Remove(p.CACert); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p.CAKey); err != nil {
		t.Fatal(err)
	}

	changed, err := Ensure(p)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("Ensure reported no change after the CA was removed")
	}

	newLeaf := readCert(t, p.LeafCert)
	if string(newLeaf.Raw) == string(oldLeaf) {
		t.Fatal("the leaf was kept even though it no longer chains to the CA")
	}
	roots := x509.NewCertPool()
	roots.AddCert(readCert(t, p.CACert))
	if _, err := newLeaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("reissued leaf does not chain to the new CA: %v", err)
	}
}

func TestInspectReportsMissingCredentials(t *testing.T) {
	s := Inspect(At(t.TempDir()))
	if s.CAExists || s.LeafExists {
		t.Errorf("Inspect found credentials in an empty directory: %+v", s)
	}
	if s.KeyModeOK {
		t.Error("Inspect reported key permissions OK with no keys present")
	}
}

func TestInspectReportsGeneratedCredentials(t *testing.T) {
	p := ensureIn(t, t.TempDir())
	s := Inspect(p)

	if !s.CAExists || !s.LeafExists {
		t.Fatalf("Inspect missed generated credentials: %+v", s)
	}
	if !s.LeafExpiry {
		t.Error("a freshly generated leaf is reported as expired")
	}
	if !s.KeyModeOK {
		t.Error("freshly written keys are reported as wrongly permissioned")
	}
}
