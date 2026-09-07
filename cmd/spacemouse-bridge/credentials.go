package main

import (
	"fmt"
	"log/slog"

	"github.com/kchellappan/spacemouse_linux_ws/internal/certs"
)

// resolveCertDir picks the credential directory: the flag if given, otherwise
// the per-user default under XDG_DATA_HOME.
func resolveCertDir(dir string) (certs.Paths, error) {
	if dir == "" {
		d, err := certs.DefaultDir()
		if err != nil {
			return certs.Paths{}, err
		}
		dir = d
	}
	return certs.At(dir), nil
}

// ensureCredentials generates the CA and leaf if they are missing or close to
// expiry, and returns the chain and key to serve with.
//
// Generation happens here, in the user's session, rather than in the package's
// postinst: NSS stores and this key are per-user, the installing root has no
// business creating them, and a CA private key shipped inside a .deb would be
// extractable by anyone who downloads the release.
func ensureCredentials(log *slog.Logger, p certs.Paths) (certFile, keyFile string, err error) {
	changed, err := certs.Ensure(p)
	if err != nil {
		return "", "", fmt.Errorf("preparing TLS credentials in %s: %w", p.Dir, err)
	}
	if changed {
		log.Info("generated TLS credentials", "dir", p.Dir)
	}
	return p.FullChain, p.LeafKey, nil
}

// ensureTrust installs the CA into every browser NSS store that lacks it.
//
// This runs on every start because browser profiles come and go: a profile
// created after the last run would otherwise reject the bridge with no
// explanation. Installing into a store that already has it is skipped, so the
// common case does no work.
//
// Failure is reported, not fatal. A bridge that serves and warns is more
// useful than one that refuses to start, and the user can run -trust later.
func ensureTrust(log *slog.Logger, p certs.Paths) {
	if !certs.CertutilAvailable() {
		log.Warn("cannot manage browser trust", "err", certs.ErrNoCertutil)
		return
	}

	stores, err := certs.FindStores()
	if err != nil {
		log.Warn("cannot enumerate browser profiles", "err", err)
		return
	}
	// Chromium's shared database often does not exist until something has
	// written to it, and clicking through a warning does not create it.
	if s, created, err := certs.EnsureChromiumStore(); err != nil {
		log.Debug("no Chromium trust store", "err", err)
	} else {
		if created {
			log.Info("created Chromium trust store", "dir", s.Dir)
		}
		stores = appendStore(stores, s)
	}

	var installed int
	for _, s := range stores {
		if certs.Trusted(s, p.CACert) {
			continue
		}
		if err := certs.Install(s, p.CACert); err != nil {
			log.Warn("could not install the CA", "store", s.Dir, "label", s.Label, "err", err)
			continue
		}
		log.Info("installed the local CA", "store", s.Dir, "label", s.Label)
		installed++
	}

	// NSS reads its database at process start, so a browser that was already
	// running will not see what we just wrote.
	if installed > 0 {
		if running := certs.RunningBrowsers(); len(running) > 0 {
			log.Warn("restart these browsers for the new certificate to take effect",
				"browsers", running)
		}
	}
}

// appendStore adds s unless the same directory is already listed.
func appendStore(stores []certs.Store, s certs.Store) []certs.Store {
	for _, existing := range stores {
		if existing.Dir == s.Dir {
			return stores
		}
	}
	return append(stores, s)
}

// runTrust installs the CA into every browser store, generating credentials
// first if needed.
func runTrust(log *slog.Logger, p certs.Paths) error {
	if !certs.CertutilAvailable() {
		return certs.ErrNoCertutil
	}
	if _, _, err := ensureCredentials(log, p); err != nil {
		return err
	}
	ensureTrust(log, p)
	return nil
}

// runUntrust removes the CA from every browser store. The credential files are
// left alone: removing trust is about undoing what we did to the browsers, and
// deleting the key would force a new CA — and a fresh round of injection — the
// next time the bridge starts.
func runUntrust(log *slog.Logger, p certs.Paths) error {
	if !certs.CertutilAvailable() {
		return certs.ErrNoCertutil
	}
	stores, err := certs.FindStores()
	if err != nil {
		return err
	}
	if s, _, err := certs.EnsureChromiumStore(); err == nil {
		stores = appendStore(stores, s)
	}

	var removed int
	for _, s := range stores {
		if !certs.Trusted(s, p.CACert) {
			continue
		}
		if err := certs.Remove(s); err != nil {
			log.Warn("could not remove the CA", "store", s.Dir, "err", err)
			continue
		}
		log.Info("removed the local CA", "store", s.Dir, "label", s.Label)
		removed++
	}
	if removed == 0 {
		log.Info("the local CA was not present in any browser trust store")
	}
	log.Info("credential files left in place", "dir", p.Dir)
	return nil
}
