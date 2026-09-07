package certs

import (
	"os/exec"
	"testing"
)

// newStore builds an empty NSS database, the way EnsureChromiumStore does for
// a machine where no browser has written one yet.
func newStore(t *testing.T) Store {
	t.Helper()
	if !CertutilAvailable() {
		t.Skip("certutil not installed (apt install libnss3-tools)")
	}
	dir := t.TempDir()
	if out, err := exec.Command("certutil", "-N", "--empty-password", "-d", "sql:"+dir).CombinedOutput(); err != nil {
		t.Fatalf("creating NSS database: %v: %s", err, out)
	}
	return Store{Dir: dir, Label: "test"}
}

func TestInstallAndRemoveRoundTrip(t *testing.T) {
	s := newStore(t)
	p := ensureIn(t, t.TempDir())

	if Trusted(s, p.CACert) {
		t.Fatal("a fresh store reports our CA as trusted")
	}
	if err := Install(s, p.CACert); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !Trusted(s, p.CACert) {
		t.Fatal("the CA is not trusted after Install")
	}

	// Installing twice is what every service start does.
	if err := Install(s, p.CACert); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if !Trusted(s, p.CACert) {
		t.Fatal("the CA is not trusted after a repeat Install")
	}

	if err := Remove(s); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if Trusted(s, p.CACert) {
		t.Fatal("the CA is still trusted after Remove")
	}
	if err := Remove(s); err != nil {
		t.Errorf("removing an absent CA should be a no-op, got %v", err)
	}
}

// Regression: trust used to be decided by nickname alone. Delete the
// credential directory and a new CA is generated under the same name, so a
// store holding the old one looked trusted, the new CA was never installed,
// and the browser rejected the bridge with nothing to explain why.
func TestTrustedComparesTheCertificateNotTheNickname(t *testing.T) {
	s := newStore(t)
	old := ensureIn(t, t.TempDir())
	fresh := ensureIn(t, t.TempDir())

	if err := Install(s, old.CACert); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if Trusted(s, fresh.CACert) {
		t.Error("a different CA under the same nickname was reported as trusted")
	}
	if !present(s) {
		t.Error("the old CA is filed under our nickname but present() missed it")
	}

	// Install must replace, not duplicate or fail.
	if err := Install(s, fresh.CACert); err != nil {
		t.Fatalf("replacing the CA: %v", err)
	}
	if !Trusted(s, fresh.CACert) {
		t.Error("the new CA is not trusted after replacing the old one")
	}
	if Trusted(s, old.CACert) {
		t.Error("the old CA is still trusted after being replaced")
	}
}

func TestTrustedIsFalseWhenTheCAFileIsMissing(t *testing.T) {
	s := newStore(t)
	if Trusted(s, "/nonexistent/ca.pem") {
		t.Error("Trusted reported true for a CA file that does not exist")
	}
}
