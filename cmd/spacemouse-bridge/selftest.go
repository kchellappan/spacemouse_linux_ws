package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kchellappan/spacemouse_linux_ws/internal/certs"
	"github.com/kchellappan/spacemouse_linux_ws/internal/config"
	"github.com/kchellappan/spacemouse_linux_ws/internal/server"
	"github.com/kchellappan/spacemouse_linux_ws/internal/spacenav"
)

// Installs are manual and there is no configuration management behind them, so
// the difference between a bug report and a self-service fix is a command that
// says which link in the chain is broken. See docs/08-distribution-plan.md.

type checkStatus int

const (
	statusOK checkStatus = iota
	statusWarn
	statusFail
)

func (s checkStatus) String() string {
	switch s {
	case statusOK:
		return "ok"
	case statusWarn:
		return "warn"
	default:
		return "FAIL"
	}
}

type check struct {
	name   string
	status checkStatus
	detail string
	// hint is what to do about it, printed only when something is wrong.
	hint string
}

type report struct{ checks []check }

func (r *report) add(name string, status checkStatus, detail, hint string) {
	r.checks = append(r.checks, check{name, status, detail, hint})
}

func (r *report) ok(name, detail string)                  { r.add(name, statusOK, detail, "") }
func (r *report) warn(name, detail, hint string)          { r.add(name, statusWarn, detail, hint) }
func (r *report) fail(name, detail, hint string)          { r.add(name, statusFail, detail, hint) }
func (r *report) addErr(name string, err error, h string) { r.fail(name, err.Error(), h) }

// runSelftest prints a checklist and returns an error when something is
// broken. Warnings do not fail: a running browser or a missing device is
// ordinary, and exiting non-zero for them would make the command useless in
// scripts.
func runSelftest(p certs.Paths, socket, cfgPath string, settings config.Config) error {
	var r report

	checkSettings(&r, cfgPath, settings)
	checkSpacenavd(&r, socket)
	checkDeviceNode(&r)
	checkPort(&r)
	checkCredentials(&r, p)
	checkTrust(&r, p)

	width := 0
	for _, c := range r.checks {
		if len(c.name) > width {
			width = len(c.name)
		}
	}

	fmt.Println()
	fmt.Println("spacemouse-bridge selftest")
	fmt.Println()
	var failed int
	for _, c := range r.checks {
		fmt.Printf("  [%-4s] %-*s  %s\n", c.status, width, c.name, c.detail)
		if c.status != statusOK && c.hint != "" {
			fmt.Printf("         %-*s  -> %s\n", width, "", c.hint)
		}
		if c.status == statusFail {
			failed++
		}
	}
	fmt.Println()

	if failed > 0 {
		return fmt.Errorf("%d of %d checks failed", failed, len(r.checks))
	}
	return nil
}

// checkSettings surfaces the trap this whole package exists to close: a
// device whose full scale was never measured runs against the built-in 350
// and feels sluggish, with nothing anywhere saying so.
func checkSettings(r *report, path string, c config.Config) {
	if _, err := os.Stat(path); err != nil {
		r.warn("settings", "no file at "+shortenHome(path)+"; using built-in defaults",
			"run -calibrate to measure this device and write one")
		return
	}
	if c.FullScale == config.Default().FullScale {
		r.warn("settings", fmt.Sprintf("%s, but full scale is still the built-in %g",
			shortenHome(path), c.FullScale),
			"run -calibrate; an unmeasured full scale costs sensitivity")
		return
	}
	r.ok("settings", fmt.Sprintf("%s, full scale %g", shortenHome(path), c.FullScale))
}

func checkSpacenavd(r *report, socket string) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	c, err := spacenav.Dial(socket, quiet)
	if err != nil {
		r.fail("spacenavd", err.Error(),
			"sudo apt install spacenavd && sudo systemctl enable --now spacenavd")
		return
	}
	defer c.Close()

	where := socket
	if where == "" {
		where = strings.Join(spacenav.SocketPaths, " or ")
	}
	r.ok("spacenavd", "connected on "+where)
}

// checkDeviceNode looks for a 3Dconnexion device by its udev name. spacenavd
// serves its socket whether or not hardware is attached, so a connectable
// socket alone does not prove there is a puck to read.
func checkDeviceNode(r *report) {
	matches, _ := filepath.Glob("/dev/input/by-id/*3Dconnexion*")
	if len(matches) == 0 {
		matches, _ = filepath.Glob("/dev/input/by-id/*SpaceMouse*")
	}
	if len(matches) == 0 {
		r.warn("device", "no 3Dconnexion device node found",
			"plug the SpaceMouse in; this check reads /dev/input/by-id and can miss unusual models")
		return
	}
	r.ok("device", filepath.Base(matches[0]))
}

func checkPort(r *report) {
	addr := net.JoinHostPort(server.DefaultHost, fmt.Sprint(server.DefaultPort))

	ln, err := net.Listen("tcp", addr)
	if err == nil {
		ln.Close()
		r.ok("port", addr+" is free")
		return
	}

	// Something holds it. Whether that is a problem depends entirely on
	// whether the holder speaks our discovery endpoint.
	if version, ok := probeNLProxy(addr); ok {
		r.ok("port", fmt.Sprintf("%s already serving the discovery endpoint (version %s)", addr, version))
		return
	}
	r.fail("port", addr+" is held by something that is not a bridge",
		"find it with: ss -ltnp 'sport = :"+fmt.Sprint(server.DefaultPort)+"'")
}

// probeNLProxy asks the discovery endpoint who it is. The certificate is not
// verified: we are identifying the listener, not trusting it.
func probeNLProxy(addr string) (string, bool) {
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec
	}
	resp, err := client.Get("https://" + addr + "/3dconnexion/nlproxy")
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var body struct {
		Port    int    `json:"port"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body); err != nil {
		return "", false
	}
	if body.Version == "" {
		return "", false
	}
	return body.Version, true
}

func checkCredentials(r *report, p certs.Paths) {
	s := certs.Inspect(p)
	switch {
	case !s.CAExists:
		r.warn("certificates", "none generated yet in "+p.Dir,
			"they are created on first start; or run: spacemouse-bridge -trust")
		return
	case !s.LeafExists:
		r.fail("certificates", "CA present but the leaf is missing",
			"spacemouse-bridge -trust")
		return
	case !s.LeafExpiry:
		r.fail("certificates", "the leaf expired on "+s.LeafNotAfter.Format(time.DateOnly),
			"spacemouse-bridge -trust regenerates it")
		return
	}
	r.ok("certificates", fmt.Sprintf("CA valid to %s, leaf to %s",
		s.CAExpiry.Format(time.DateOnly), s.LeafNotAfter.Format(time.DateOnly)))

	// A private key another local account can read defeats the point of
	// installing this CA into the browser at all.
	if !s.KeyModeOK {
		r.fail("key permissions", "a private key in "+p.Dir+" is not mode 0600",
			"chmod 600 "+p.Dir+"/*-key.pem")
		return
	}
	r.ok("key permissions", "private keys are 0600")
}

func checkTrust(r *report, p certs.Paths) {
	if !certs.CertutilAvailable() {
		r.fail("certutil", "not installed", "sudo apt install libnss3-tools")
		return
	}
	r.ok("certutil", "available")

	stores, err := certs.FindStores()
	if err != nil {
		r.addErr("browser profiles", err, "")
		return
	}
	// Read-only on purpose: a check that creates a trust store would report a
	// state it made up rather than the one the user has.
	if dir, err := certs.ChromiumStore(); err == nil {
		if _, err := os.Stat(filepath.Join(dir, "cert9.db")); err == nil {
			stores = appendStore(stores, certs.Store{Dir: dir, Label: "Chromium family"})
		}
	}
	if len(stores) == 0 {
		r.warn("browser profiles", "none found",
			"start a browser once so it creates a profile, then run -trust")
		return
	}

	var missing []string
	for _, s := range stores {
		if !certs.Trusted(s, p.CACert) {
			missing = append(missing, shortenHome(s.Dir))
		}
	}
	sort.Strings(missing)

	if len(missing) == 0 {
		r.ok("browser trust", fmt.Sprintf("the CA is installed in all %d profile(s)", len(stores)))
	} else {
		r.warn("browser trust",
			fmt.Sprintf("missing from %d of %d profile(s): %s",
				len(missing), len(stores), strings.Join(missing, ", ")),
			"spacemouse-bridge -trust, or just restart the service")
	}

	// NSS reads its database once, at process start.
	if running := certs.RunningBrowsers(); len(running) > 0 {
		r.warn("running browsers", strings.Join(running, ", "),
			"a browser that was already running will not see a newly installed CA until it restarts")
	} else {
		r.ok("running browsers", "none")
	}
}

// shortenHome trims the home directory to ~ so the report stays readable.
func shortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || !strings.HasPrefix(path, home) {
		return path
	}
	return "~" + strings.TrimPrefix(path, home)
}
