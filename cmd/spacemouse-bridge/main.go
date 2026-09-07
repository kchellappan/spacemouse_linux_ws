// Command spacemouse-bridge exposes a 3Dconnexion SpaceMouse to browser-based
// CAD applications over the loopback WebSocket interface that 3Dconnexion's
// 3DconnexionJS client library expects.
//
// See docs/ for protocol, certificate and distribution notes.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kchellappan/spacemouse_linux_ws/internal/config"
	"github.com/kchellappan/spacemouse_linux_ws/internal/server"
	"github.com/kchellappan/spacemouse_linux_ws/internal/spacenav"
)

var version = "dev"

// deviceWait covers the startup race between this user service and spacenavd.
// Longer absences are systemd's problem: the unit restarts us.
const deviceWait = 30 * time.Second

func main() {
	var (
		host       = flag.String("host", server.DefaultHost, "address to bind")
		port       = flag.Int("port", server.DefaultPort, "port to bind")
		cert       = flag.String("cert", "", "TLS certificate chain (leaf + CA) in PEM form")
		key        = flag.String("key", "", "TLS private key in PEM form")
		debug      = flag.Bool("debug", false, "log every WAMP frame")
		mode       = flag.String("mode", "drive", "what to do on connect: drive, probe, orbit, or none")
		orbitSpeed = flag.Float64("orbit-speed", 30, "degrees per second for -mode=orbit")
		readMouse  = flag.Bool("read-mouse", false, "dump spacenavd events and exit; does not start the server")
		calibrate  = flag.Bool("calibrate", false, "identify which physical motion drives which axis, then exit")
		sockPath   = flag.String("spnav-socket", "", "spacenavd socket path (default: well-known locations)")

		certDir     = flag.String("cert-dir", "", "where the generated CA and leaf live (default: $XDG_DATA_HOME/spacemouse-bridge)")
		doTrust     = flag.Bool("trust", false, "install the local CA into every browser profile found, then exit")
		doUntrust   = flag.Bool("untrust", false, "remove the local CA from every browser profile found, then exit")
		doSelftest  = flag.Bool("selftest", false, "check the installation and report, then exit")
		noAutoTrust = flag.Bool("no-auto-trust", false, "serve without installing the CA into browser profiles at startup")

		showVersion = flag.Bool("version", false, "print the version and exit")

		configPath = flag.String("config", "", "settings file (default: $XDG_CONFIG_HOME/spacemouse-bridge/config.json)")
		showConfig = flag.Bool("show-config", false, "print the effective settings as JSON and exit")

		navMode     = flag.String("nav-mode", "object", "object: the model follows the cap; camera: the camera does")
		fullScale   = flag.Float64("full-scale", 350, "device units at full deflection, from -calibrate")
		deadzone    = flag.Float64("deadzone", 0.06, "fraction of full scale to ignore")
		exponent    = flag.Float64("curve", 1.6, "response curve; 1 is linear, higher gives finer control near centre")
		transSpeed  = flag.Float64("pan-speed", 0.9, "model diagonals per second at full deflection")
		rotSpeed    = flag.Float64("rotate-speed", 1.6, "radians per second at full deflection")
		zoomSpeed   = flag.Float64("zoom-speed", 1.2, "orthographic zoom, e-foldings per second at full deflection")
		dominant    = flag.Bool("dominant-axis", false, "use only the strongest axis; suppresses cross-talk")
		noRotate    = flag.Bool("no-rotate", false, "disable rotation, leaving pan and zoom only")
		noTranslate = flag.Bool("no-translate", false, "disable translation, leaving rotation only")
		frameRate   = flag.Int("frame-rate", 60, "camera updates per second when we drive the clock")
		buttons     = flag.String("buttons", "0=fit,1=menu", "button mapping, e.g. 0=fit,1=menu; actions: none, fit, menu, dominant-axis, rotation-lock")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfgPath := *configPath
	if cfgPath == "" {
		p, err := config.DefaultPath()
		if err != nil {
			log.Error("cannot locate the settings file", "err", err)
			os.Exit(1)
		}
		cfgPath = p
	}
	settings, err := config.Load(cfgPath)
	if err != nil {
		// A corrupt file should not silently reset a user's tuning.
		log.Error("cannot read settings", "err", err)
		os.Exit(1)
	}
	// Flags beat the file, but only where the user actually passed one:
	// flag.Visit reports set flags, not defaulted ones.
	setFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })
	applyFlagOverrides(&settings, setFlags, flagValues{
		navMode: *navMode, fullScale: *fullScale, deadzone: *deadzone,
		curve: *exponent, panSpeed: *transSpeed, rotateSpeed: *rotSpeed,
		zoomSpeed: *zoomSpeed, dominantAxis: *dominant,
		noRotate: *noRotate, noTranslate: *noTranslate,
		frameRate: *frameRate, buttons: *buttons,
	})

	if *showConfig {
		if err := printConfig(settings, cfgPath); err != nil {
			log.Error("cannot print settings", "err", err)
			os.Exit(1)
		}
		return
	}

	if *calibrate {
		if err := runCalibrate(*sockPath, cfgPath, settings, log); err != nil {
			log.Error("calibration failed", "err", err)
			os.Exit(1)
		}
		return
	}

	if *readMouse {
		if err := dumpMouse(*sockPath, log); err != nil {
			log.Error("read-mouse failed", "err", err)
			os.Exit(1)
		}
		return
	}

	paths, err := resolveCertDir(*certDir)
	if err != nil {
		log.Error("cannot locate the credential directory", "err", err)
		os.Exit(1)
	}

	switch {
	case *doSelftest:
		if err := runSelftest(paths, *sockPath, cfgPath, settings); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	case *doTrust:
		if err := runTrust(log, paths); err != nil {
			log.Error("trust failed", "err", err)
			os.Exit(1)
		}
		return
	case *doUntrust:
		if err := runUntrust(log, paths); err != nil {
			log.Error("untrust failed", "err", err)
			os.Exit(1)
		}
		return
	}

	// Explicit -cert/-key means the caller is managing credentials itself, so
	// keep hands off both the files and the browser trust stores. Otherwise
	// this is the packaged service, which is expected to arrive at a working
	// browser on its own.
	certFile, keyFile := *cert, *key
	switch {
	case (*cert == "") != (*key == ""):
		log.Error("-cert and -key must be given together")
		os.Exit(2)
	case *cert == "":
		certFile, keyFile, err = ensureCredentials(log, paths)
		if err != nil {
			log.Error("cannot prepare TLS credentials", "err", err)
			os.Exit(1)
		}
		if !*noAutoTrust {
			ensureTrust(log, paths)
		}
	}

	opts := server.Options{
		Log:         log,
		ServerIdent: "spacemouse-bridge " + version,
	}
	// deviceDead is nil outside drive mode, and a nil channel blocks forever
	// in a select, which is exactly the behaviour we want there.
	var deviceDead <-chan struct{}
	var device *spacenav.Client

	switch *mode {
	case "drive":
		// At login this can start before spacenavd has created its socket, so
		// wait rather than exiting into a systemd restart loop. A device that
		// is simply not plugged in still exits, and the unit restarts us.
		dev, err := spacenav.WaitForDevice(*sockPath, deviceWait, log)
		if err != nil {
			log.Error("cannot reach the SpaceMouse", "err", err)
			os.Exit(1)
		}
		defer dev.Close()
		device, deviceDead = dev, dev.Dead()

		btns, err := server.ParseButtons(settings.ButtonSpec())
		if err != nil {
			log.Error("bad button mapping", "err", err)
			os.Exit(2)
		}
		if settings.NavMode != "object" && settings.NavMode != "camera" {
			log.Error("unknown nav mode", "mode", settings.NavMode, "want", "object or camera")
			os.Exit(2)
		}

		if settings.FullScale == config.Default().FullScale {
			log.Warn("using the built-in full scale; run -calibrate to measure this device",
				"fullScale", settings.FullScale)
		}

		opts.OnReady = server.Drive(log, server.DriveOptions{
			Device:    dev,
			Config:    settings.Nav(),
			FrameRate: settings.FrameRate,
			Buttons:   btns,
		})
	case "probe":
		opts.OnReady = server.Probe(log)
	case "orbit":
		opts.OnReady = server.Orbit(log, *orbitSpeed)
	case "none":
	default:
		log.Error("unknown -mode", "mode", *mode, "want", "drive, probe, orbit or none")
		os.Exit(2)
	}

	addr := net.JoinHostPort(*host, fmt.Sprint(*port))
	srv := &http.Server{
		Addr:              addr,
		Handler:           server.New(opts),
		ReadHeaderTimeout: 10 * time.Second,
		// Force HTTP/1.1. WebSocket over HTTP/2 needs RFC 8441 Extended
		// CONNECT, which the upgrader cannot hijack; browsers use HTTP/1.1
		// for WebSocket anyway, and the real NL-Proxy is HTTP/1.1 only.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
		// Browsers open and abandon TLS connections routinely, and the
		// stdlib logs each one as "TLS handshake error ... EOF" at the top
		// level. In a journal that reads like a fault. Route the server's own
		// chatter through slog at debug, where -debug can still surface it.
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Losing spacenavd is not recoverable in place: the read loop is gone, and
	// the drive loop returns the moment its event channel closes. Left alone
	// the process would keep serving, browsers would keep connecting, and the
	// puck would do nothing — a live service with dead navigation, which is
	// the worst of the available failures. Exit instead, and let the unit's
	// Restart=always reconnect.
	// atomic: written by the watcher goroutine, read by main after the server
	// returns. The happens-before edge through Shutdown is real but subtle,
	// and not worth relying on.
	var deviceLost atomic.Bool
	go func() {
		select {
		case <-ctx.Done():
		case <-deviceDead:
			if err := device.Err(); err != nil {
				deviceLost.Store(true)
				log.Error("lost the connection to spacenavd; exiting so the service restarts", "err", err)
			} else {
				// Close was called; the ordinary shutdown path is running.
				return
			}
		}
		sc, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sc)
	}()

	log.Info("listening", "url", "https://"+addr, "version", version)
	err = srv.ListenAndServeTLS(certFile, keyFile)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server failed", "err", err)
		os.Exit(1)
	}
	if deviceLost.Load() {
		os.Exit(1)
	}
	log.Info("stopped")
}

// dumpMouse prints device events, so a user can confirm spacenavd and the
// hardware work before involving a browser.
func dumpMouse(socket string, log *slog.Logger) error {
	c, err := spacenav.Dial(socket, log)
	if err != nil {
		return err
	}
	defer c.Close()

	log.Info("reading spacenavd events; move the puck (ctrl-c to stop)")
	for ev := range c.Events() {
		switch {
		case ev.Motion != nil:
			m := ev.Motion
			log.Info("motion",
				"x", m.X, "y", m.Y, "z", m.Z,
				"rx", m.RX, "ry", m.RY, "rz", m.RZ,
				"periodMs", m.Period, "centred", m.Zero())
		case ev.Button != nil:
			log.Info("button", "id", ev.Button.ID, "pressed", ev.Button.Pressed)
		}
	}
	return nil
}

// gesture is one prompt in the calibration walkthrough.
type gesture struct {
	name    string
	prompt  string
	diagram string
	// rotation marks the three gestures that tip or twist the cap. Sliding
	// and tipping do not share a range, so their peaks are pooled separately.
	rotation bool
}

// The six degrees of freedom, described physically. Wording matters: the whole
// point is to remove ambiguity about what the user actually did.
// The cap travels only a millimetre or two in every direction, so the prompts
// say what to do with the cap rather than with the device, and each carries a
// diagram: describing a 6-DoF motion in words alone is genuinely ambiguous.
var gestures = []gesture{
	{"translate right", "SLIDE the cap RIGHT", `
        side view                    cap stays level,
                                     slides sideways
        ╭─────────────╮
        │     cap     │  ═══►
        ╰─────────────╯
       ╭───────────────╮
       │     base      │
       ╰───────────────╯`, false},

	{"translate up", "PULL the cap UP", `
        side view                    grip the sides and lift.
              ▲                      travel is 1-2 mm, so it
              ║                      will feel like nothing
        ╭─────────────╮              is happening — watch the
        │     cap     │              peak readout, not the feel
        ╰─────────────╯
       ╭───────────────╮
       │     base      │
       ╰───────────────╯`, false},

	{"translate away", "PUSH the cap AWAY from you", `
        top view                     cap stays level,
                                     slides toward
          ▲  away / screen           the screen
          ║
        ╭─────────────╮
        │      ●      │
        ╰─────────────╯
             toward you`, false},

	{"pitch forward", "TIP the cap FORWARD, far edge down", `
        side view, screen on the left

        ╭─────────────╮          ╭────────╮
        │     cap     │  ═══►   ╱         ╰╮
        ╰─────────────╯        ╰───────────╯
            level              far edge DOWN,
                               near edge UP`, true},

	{"yaw right", "TWIST the cap CLOCKWISE", `
        top view

        ╭─────────────╮
        │      ↻      │          rotate about the
        ╰─────────────╯          vertical axis`, true},

	{"roll right", "TIP the cap RIGHT, right edge down", `
        front view, facing you

        ╭─────────────╮          ╭────────╮
        │     cap     │  ═══►   ╱         ╰╮
        ╰─────────────╯        ╰───────────╯
            level              right edge DOWN,
                               left edge UP`, true},
}

// calibrateDominance is stricter than Deflection.Clean: for calibration we
// want an unambiguous reading, and cross-talk on a SpaceMouse is easy to
// produce accidentally.
const calibrateDominance = 3

// runCalibrate walks the six gestures and prints the resulting axis map.
// Device models and spnavrc settings both change this, so it is measured
// rather than assumed. See docs/05-spacenavd.md.
func runCalibrate(socket, cfgPath string, settings config.Config, log *slog.Logger) error {
	c, err := spacenav.Dial(socket, log)
	if err != nil {
		return err
	}
	defer c.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := spacenav.DefaultDetectOptions

	fmt.Println()
	fmt.Println("SpaceMouse axis calibration")
	fmt.Println()

	// A real puck rests with a small residual offset rather than at exact
	// zero. Measure it, so "has the user let go" has a sane threshold and so
	// we know the floor the navigation model must ignore.
	fmt.Print("  [noise] TAKE YOUR HAND OFF the puck ... ")
	time.Sleep(1500 * time.Millisecond)
	floor, err := spacenav.MeasureNoiseFloor(ctx, c, 2*time.Second)
	if err != nil {
		fmt.Println()
		return err
	}
	// The floor at true rest understates how far the cap sits just after a
	// push: the spring settles slowly. Keep generous headroom.
	opts.RestThreshold = restThreshold(floor, 0)
	fmt.Printf("resting noise floor = %d\n", floor)

	// A provisional scale, only so gesture detection has a sane threshold.
	// The scale that ends up in the settings file comes from the six gestures
	// below, where we know which half of the device is being exercised.
	fmt.Println()
	fmt.Println("  [scale] Push the cap as FAR AS IT GOES, any direction, and HOLD until")
	fmt.Println("          the reading stops climbing, then release.")
	fmt.Print("        ")
	if err := spacenav.WaitCentred(ctx, c, opts.CentreTimeout, opts.RestThreshold); err != nil {
		fmt.Println()
		return err
	}
	full, err := spacenav.DetectDeflection(ctx, c, opts, pushMeter())
	if err != nil {
		fmt.Println()
		return err
	}
	fullScale := full.Peak
	opts.Threshold = fullScale * 35 / 100
	if opts.Threshold < 20 {
		opts.Threshold = 20
	}
	opts.RestThreshold = restThreshold(floor, fullScale)
	fmt.Printf("\r  [scale] provisional full deflection = %d; push threshold %d, released below %d%s\n",
		fullScale, opts.Threshold, opts.RestThreshold, strings.Repeat(" ", 20))

	if floor*10 > fullScale {
		fmt.Printf("\n  NOTE: the resting offset is %s of full scale. Consider raising\n",
			pct(floor, fullScale))
		fmt.Println("        dead-zone in /etc/spnavrc, or the view will drift when idle.")
	}

	fmt.Println()
	fmt.Println("  Now one motion at a time. Push firmly, hold, then let go and let")
	fmt.Println("  the cap re-centre before the next prompt.")

	results := make([]spacenav.Deflection, len(gestures))
	for i, g := range gestures {
		fmt.Printf("\n  [%d/%d] %s\n%s\n\n", i+1, len(gestures), g.prompt, g.diagram)

		for attempt := 1; ; attempt++ {
			if attempt > 1 {
				fmt.Printf("        attempt %d — ", attempt)
			}
			// Say what we are waiting for: a cap that has not settled would
			// otherwise look like a hang.
			fmt.Print("        waiting for the cap to centre ...")

			if err := spacenav.WaitCentred(ctx, c, opts.CentreTimeout, opts.RestThreshold); err != nil {
				fmt.Println()
				return err
			}
			fmt.Printf("\r        now do the motion ...%s", strings.Repeat(" ", 12))
			d, err := spacenav.DetectDeflection(ctx, c, opts, pushMeter())
			if errors.Is(err, spacenav.ErrNoDeflection) {
				fmt.Printf("\r        skipped: nothing detected in %s%s\n",
					opts.DetectTimeout, strings.Repeat(" ", 20))
				results[i] = spacenav.Deflection{}
				break
			}
			if err != nil {
				fmt.Println()
				return err
			}
			fmt.Printf("\r        %s%s\n", d, strings.Repeat(" ", 24))

			if d.RunnerUp*calibrateDominance < d.Peak || attempt >= 3 {
				results[i] = d
				if d.RunnerUp*calibrateDominance >= d.Peak {
					fmt.Printf("        accepted after %d attempts, but axes stayed mixed\n", attempt)
				}
				break
			}
			fmt.Printf("        %s cross-talk from %s — try again, more purely\n",
				pct(d.RunnerUp, d.Peak), d.RunnerUpAxis)
		}
	}

	printAxisMap(results)

	// Full scale comes from the six deliberate gestures, not the opening
	// improvised push. That push measured whichever half of the device the
	// user happened to move: the same SpaceMouse Compact reported 216 one run
	// and 146 the next, a 48% swing landing straight in the feel, because
	// FullScale is a divisor. The six peaks were already being measured and
	// thrown away.
	transScale := groupScale(results, false)
	rotScale := groupScale(results, true)

	if transScale == 0 {
		fmt.Printf("    No translation gesture was detected; falling back to the\n")
		fmt.Printf("    provisional %d. Re-run and slide the cap more firmly.\n\n", fullScale)
		transScale = fullScale
	}
	if rotScale == 0 {
		fmt.Printf("    No rotation gesture was detected; falling back to the\n")
		fmt.Printf("    provisional %d. Re-run and tip the cap more firmly.\n\n", fullScale)
		rotScale = fullScale
	}

	fmt.Printf("    Full deflection: %d sliding, %d tipping. Resting noise floor %d.\n\n",
		transScale, rotScale, floor)

	// Say whether each number is the device's limit or merely how hard the
	// user pushed. Without this the output looks equally authoritative either
	// way, which is how two runs of this command produced 216 and 146 with
	// nothing on screen suggesting either was wrong.
	reportConfidence(results, false, "Sliding")
	reportConfidence(results, true, "Tipping")

	if ratio := scaleRatio(transScale, rotScale); ratio >= 1.4 {
		fmt.Printf("    The halves differ by %.1fx. spnavrc tunes\n", ratio)
		fmt.Printf("    sensitivity-translation and sensitivity-rotation separately, so\n")
		fmt.Printf("    that is expected; each half is normalised against its own.\n\n")
	}

	// Persist it. Measuring a device and then printing the number at someone
	// was the old behaviour, and it meant every install ran against the
	// built-in full scale forever.
	settings.FullScale = float64(transScale)
	settings.RotationFullScale = float64(rotScale)
	settings.NoiseFloor = float64(floor)
	fullScale = transScale

	// A dead zone below the resting offset lets the view drift while nobody
	// is touching the puck, so a measurement that demands a wider one wins.
	if fullScale > 0 {
		needed := float64(floor) * 1.5 / float64(fullScale)
		if needed > settings.Deadzone {
			fmt.Printf("    Raising the dead zone from %.3f to %.3f: the resting offset\n",
				settings.Deadzone, needed)
			fmt.Printf("    would otherwise drift the view when idle.\n\n")
			settings.Deadzone = needed
		}
	}

	if err := saveSettings(cfgPath, settings); err != nil {
		return fmt.Errorf("saving calibration: %w", err)
	}
	fmt.Println("    Restart the service to pick this up:")
	fmt.Println("      systemctl --user restart spacemouse-bridge")
	fmt.Println()
	return nil
}

// reportConfidence says whether a measured full scale is the device's ceiling
// or a lower bound, and what to do about it.
func reportConfidence(results []spacenav.Deflection, rotation bool, label string) {
	peak, hits := saturated(results, rotation)
	if peak == 0 {
		return
	}
	if hits >= 2 {
		fmt.Printf("    %s reached %d on %d gestures — that is the device limit.\n",
			label, peak, hits)
		return
	}
	fmt.Printf("    %s peaked at %d on a single gesture, so it is a lower bound,\n", label, peak)
	fmt.Printf("    not a limit. If %s feels sluggish, re-run and push to the stop.\n",
		strings.ToLower(label))
}

// pushMeter shows the running peak and whether it is still climbing.
//
// A bare number cannot tell a user they have stopped short, and stopping short
// is the failure that produced a 48% swing between two calibrations of the
// same device. "still rising" versus "holding" says when to let go.
func pushMeter() func(int32) {
	var best int32
	var lastRise time.Time
	return func(peak int32) {
		now := time.Now()
		if peak > best {
			best, lastRise = peak, now
		}
		state := "still rising — keep pushing"
		if !lastRise.IsZero() && now.Sub(lastRise) > pushPlateau {
			state = "holding — release when ready"
		}
		fmt.Printf("\r        peak %-6d %-30s", peak, state)
	}
}

// pushPlateau is how long the peak must stay put before the cap counts as
// fully deflected. Short enough not to feel slow, long enough to survive the
// jitter of a hand at the mechanical limit.
const pushPlateau = 500 * time.Millisecond

// groupScale is the largest peak among the translation or rotation gestures.
// Undetected gestures have a zero peak and drop out.
func groupScale(results []spacenav.Deflection, rotation bool) int32 {
	var max int32
	for i, d := range results {
		if i >= len(gestures) || gestures[i].rotation != rotation {
			continue
		}
		if d.Peak > max {
			max = d.Peak
		}
	}
	return max
}

// saturated reports the group's peak and how many of its gestures reached that
// exact value.
//
// Two gestures landing on the identical maximum means a clamp, not a
// coincidence: spacenavd saturates at a fixed magnitude (measured at 350 on a
// SpaceMouse Compact, doc 05), so hitting it twice proves the cap was pushed
// to the limit. A lone peak below it is only a lower bound — whatever effort
// the user happened to apply — which is exactly how this device reported 216
// on one run and 146 on the next.
func saturated(results []spacenav.Deflection, rotation bool) (peak int32, hits int) {
	peak = groupScale(results, rotation)
	if peak == 0 {
		return 0, 0
	}
	for i, d := range results {
		if i < len(gestures) && gestures[i].rotation == rotation && d.Peak == peak {
			hits++
		}
	}
	return peak, hits
}

// scaleRatio reports how far apart the two halves are, larger over smaller.
func scaleRatio(a, b int32) float64 {
	if a <= 0 || b <= 0 {
		return 1
	}
	if a < b {
		a, b = b, a
	}
	return float64(a) / float64(b)
}

func pct(a, b int32) string {
	if b == 0 {
		return "0%"
	}
	return fmt.Sprintf("%d%%", a*100/b)
}

func printAxisMap(results []spacenav.Deflection) {
	fmt.Println()
	fmt.Println("  Axis map for this device:")
	fmt.Println()
	// PEAK is here because its absence hid a real problem: sliding and
	// tipping produce different magnitudes, and with only axis names on
	// screen there was nothing to notice.
	fmt.Printf("    %-18s %-6s %-6s %-7s %s\n", "GESTURE", "AXIS", "SIGN", "PEAK", "CONFIDENCE")
	for i, g := range gestures {
		if results[i].Peak == 0 {
			fmt.Printf("    %-18s %-6s %-6s %-7s %s\n", g.name, "-", "-", "-", "not detected")
			continue
		}
		sign := "+"
		if results[i].Sign < 0 {
			sign = "-"
		}
		conf := "clean"
		if results[i].RunnerUp*calibrateDominance >= results[i].Peak {
			conf = fmt.Sprintf("mixed (%s from %s)",
				pct(results[i].RunnerUp, results[i].Peak), results[i].RunnerUpAxis)
		}
		fmt.Printf("    %-18s %-6s %-6s %-7d %s\n",
			g.name, results[i].Axis, sign, results[i].Peak, conf)
	}
	fmt.Println()

	seen := map[spacenav.Axis]string{}
	conflict := false
	for i, d := range results {
		if d.Peak == 0 {
			continue
		}
		if prev, dup := seen[d.Axis]; dup {
			fmt.Printf("    WARNING: %q and %q both mapped to %s\n", prev, gestures[i].name, d.Axis)
			conflict = true
		}
		seen[d.Axis] = gestures[i].name
	}
	if conflict {
		fmt.Println("    Re-run and make each motion more distinct.")
	} else {
		fmt.Println("    All six axes distinct.")
	}
	fmt.Println()
}

// restThreshold picks the magnitude below which the cap counts as released.
//
// The noise floor sampled at true rest understates where the cap sits moments
// after a push, because the spring settles slowly — a floor of 0 was measured
// on a device that then settled at 6. Take the most generous of a multiple of
// the floor, a small fraction of full scale, and an absolute minimum.
func restThreshold(floor, fullScale int32) int32 {
	t := floor*2 + 5
	if s := fullScale * 6 / 100; s > t {
		t = s
	}
	if t < 15 {
		t = 15
	}
	return t
}
