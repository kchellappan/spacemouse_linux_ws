package certs

import (
	"bytes"
	"crypto/x509"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Store is one browser NSS database.
type Store struct {
	Dir   string // directory holding cert9.db
	Label string // a human-readable hint at which browser it belongs to
}

// FindStores locates every NSS database belonging to this user.
//
// All supported browsers keep a cert9.db, and snap/flatpak confinement only
// restricts what the *application* sees — their profile data sits at ordinary
// host paths we can write to. Globbing rather than naming browsers picks up
// LibreWolf, Floorp and future Flatpaks for free.
// See docs/04-certificates-and-browser-trust.md.
func FindStores() ([]Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	patterns := []struct{ glob, label string }{
		{filepath.Join(home, ".pki", "nssdb"), "Chromium family"},
		{filepath.Join(home, ".mozilla", "firefox", "*"), "Firefox (deb)"},
		{filepath.Join(home, "snap", "*", "common", ".mozilla", "firefox", "*"), "Firefox (snap)"},
		{filepath.Join(home, ".var", "app", "*", ".mozilla", "firefox", "*"), "Gecko (flatpak)"},
		{filepath.Join(home, ".var", "app", "*", ".zen", "*"), "Zen (flatpak)"},
	}

	seen := map[string]bool{}
	var out []Store
	for _, p := range patterns {
		matches, err := filepath.Glob(p.glob)
		if err != nil {
			continue
		}
		for _, dir := range matches {
			if seen[dir] {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, "cert9.db")); err != nil {
				continue
			}
			seen[dir] = true
			out = append(out, Store{Dir: dir, Label: p.label})
		}
	}
	return out, nil
}

// ChromiumStore is the single shared database Chromium browsers use. It often
// does not exist yet: clicking through a certificate warning stores a
// per-profile decision, not an NSS entry.
func ChromiumStore() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pki", "nssdb"), nil
}

// EnsureChromiumStore creates the Chromium database if it is missing, so the
// CA has somewhere to go before the browser has ever been run.
func EnsureChromiumStore() (Store, bool, error) {
	dir, err := ChromiumStore()
	if err != nil {
		return Store{}, false, err
	}
	s := Store{Dir: dir, Label: "Chromium family"}
	if _, err := os.Stat(filepath.Join(dir, "cert9.db")); err == nil {
		return s, false, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return s, false, fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := run("certutil", "-N", "--empty-password", "-d", "sql:"+dir); err != nil {
		return s, false, fmt.Errorf("initialising %s: %w", dir, err)
	}
	return s, true, nil
}

// CertutilAvailable reports whether the NSS tools are installed.
func CertutilAvailable() bool {
	_, err := exec.LookPath("certutil")
	return err == nil
}

// ErrNoCertutil explains the missing dependency in terms a user can act on.
var ErrNoCertutil = fmt.Errorf("certutil not found; install it with: sudo apt install libnss3-tools")

// Trusted reports whether the store holds the *same* CA as caPath.
//
// Comparing the certificate rather than just the nickname matters: if the
// credential directory is deleted, a new CA is generated under the same
// nickname, and a store still holding the old one would otherwise look
// trusted. The browser would then reject the bridge with no clue why.
func Trusted(s Store, caPath string) bool {
	installed, err := installedCert(s)
	if err != nil {
		return false
	}
	wantPEM, err := os.ReadFile(caPath)
	if err != nil {
		return false
	}
	want, err := parseCert(wantPEM)
	if err != nil {
		return false
	}
	return bytes.Equal(installed.Raw, want.Raw)
}

// present reports whether anything is filed under our nickname, whatever it
// is. Removal cares about the name; trust cares about the bytes.
func present(s Store) bool {
	_, err := installedCert(s)
	return err == nil
}

func installedCert(s Store) (*x509.Certificate, error) {
	out, err := output("certutil", "-L", "-d", "sql:"+s.Dir, "-n", Nickname, "-a")
	if err != nil {
		return nil, err
	}
	return parseCert([]byte(out))
}

// Install adds the CA to a store as a trusted issuer.
//
// The trust flags are "C,," — a certificate authority. A narrower "P,,"
// (trusted peer) works in Chromium but Gecko ignores it for server
// certificates, so a single flag that works everywhere means CA trust. This is
// safe only because the key is generated per machine and never distributed.
func Install(s Store, caPath string) error {
	// Remove any previous copy first so reinstalling is idempotent.
	_ = run("certutil", "-D", "-d", "sql:"+s.Dir, "-n", Nickname)
	if err := run("certutil", "-A", "-d", "sql:"+s.Dir, "-n", Nickname, "-t", "C,,", "-i", caPath); err != nil {
		return fmt.Errorf("installing into %s: %w", s.Dir, err)
	}
	return nil
}

// Remove deletes whatever is filed under our nickname. Absent is not an error.
func Remove(s Store) error {
	if !present(s) {
		return nil
	}
	if err := run("certutil", "-D", "-d", "sql:"+s.Dir, "-n", Nickname); err != nil {
		return fmt.Errorf("removing from %s: %w", s.Dir, err)
	}
	return nil
}

// RunningBrowsers lists browser processes that would not see a trust change.
// NSS reads its store at startup, and snap and flatpak keep content processes
// alive after the last window closes.
func RunningBrowsers() []string {
	out, err := output("pgrep", "-a", "-f", `firefox|chrome|chromium|/app/zen/zen|librewolf|floorp`)
	if err != nil {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := filepath.Base(fields[1])
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%s: %s", err, msg)
	}
	return nil
}

func output(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}
