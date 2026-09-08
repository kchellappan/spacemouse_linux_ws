package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/kchellappan/spacemouse_linux_ws/internal/certs"
	"github.com/kchellappan/spacemouse_linux_ws/internal/config"
	"github.com/kchellappan/spacemouse_linux_ws/internal/nav"
	"github.com/kchellappan/spacemouse_linux_ws/internal/navlib"
	"github.com/kchellappan/spacemouse_linux_ws/internal/server"
	"github.com/kchellappan/spacemouse_linux_ws/internal/spacenav"
	"github.com/kchellappan/spacemouse_linux_ws/internal/webui"
)

// uiState is everything the status page needs, gathered from the places that
// own it. main is the only scope that can see all of them at once.
type uiState struct {
	version   string
	startedAt time.Time
	listen    string

	device  *spacenav.Client // nil outside drive mode
	live    *config.Live
	cfgPath string
	paths   certs.Paths
	clients *server.Clients
	log     *slog.Logger

	// trust is refreshed on a timer rather than per request: each check shells
	// out to certutil once per profile, and the page polls ten times a second.
	trust *trustCache
}

// snapshot builds the payload the UI renders.
func (u *uiState) snapshot() webui.Snapshot {
	s := webui.Snapshot{
		Version:   u.version,
		StartedAt: u.startedAt,
		Listen:    u.listen,
		Settings:  u.live.Get(),
	}

	// The scales belong to the settings, not the device, so the page can draw
	// its axes to the right range even when nothing is connected.
	settings := u.live.Get()
	s.Device = webui.Device{
		FullScale:         settings.FullScale,
		RotationFullScale: settings.RotationFullScale,
	}

	if u.device != nil {
		m := u.device.State()
		s.Spacenavd = webui.Spacenavd{
			Enabled:   true,
			Connected: u.device.Err() == nil,
			Socket:    u.device.Socket(),
			Dropped:   u.device.Dropped(),
		}
		if err := u.device.Err(); err != nil {
			s.Spacenavd.Error = err.Error()
		}
		s.Device.X, s.Device.Y, s.Device.Z = m.X, m.Y, m.Z
		s.Device.RX, s.Device.RY, s.Device.RZ = m.RX, m.RY, m.RZ
		s.Device.AtRest = m.Zero()
	}

	if u.clients != nil {
		for _, c := range u.clients.Snapshot() {
			s.Clients = append(s.Clients, webui.Client{
				ID:           c.ID,
				Origin:       c.Origin,
				ConnectedAt:  c.ConnectedAt,
				Name:         c.Name,
				LibVersion:   c.LibVersion,
				MatrixLayout: c.MatrixLayout,
				FrameTiming:  c.FrameTiming,
			})
		}
	}

	st := certs.Inspect(u.paths)
	s.Certs = webui.Certs{
		Dir:        u.paths.Dir,
		CAExpiry:   st.CAExpiry,
		LeafExpiry: st.LeafNotAfter,
		KeyModeOK:  st.KeyModeOK,
		Present:    st.CAExists && st.LeafExists,
		CertutilOK: certs.CertutilAvailable(),
	}
	s.Profiles, s.Certs.RunningBrows = u.trust.get()
	return s
}

// trustCache holds the result of the certutil sweep, which is far too
// expensive to repeat on every status poll: it forks a process per browser
// profile, and the page asks ten times a second.
type trustCache struct {
	paths certs.Paths
	log   *slog.Logger

	profiles []webui.NSSStatus
	browsers []string
	updated  time.Time
	mu       chan struct{} // a one-slot mutex, so a slow sweep is skipped not queued
}

func newTrustCache(paths certs.Paths, log *slog.Logger) *trustCache {
	c := &trustCache{paths: paths, log: log, mu: make(chan struct{}, 1)}
	c.mu <- struct{}{}
	return c
}

const trustCacheTTL = 10 * time.Second

func (c *trustCache) get() ([]webui.NSSStatus, []string) {
	select {
	case <-c.mu:
	default:
		// A sweep is already running; serve what we have.
		return c.profiles, c.browsers
	}
	defer func() { c.mu <- struct{}{} }()

	if time.Since(c.updated) < trustCacheTTL {
		return c.profiles, c.browsers
	}

	var profiles []webui.NSSStatus
	if certs.CertutilAvailable() {
		stores, err := certs.FindStores()
		if err != nil {
			c.log.Debug("cannot enumerate browser profiles", "err", err)
		}
		for _, st := range stores {
			profiles = append(profiles, webui.NSSStatus{
				Dir:     st.Dir,
				Label:   st.Label,
				Trusted: certs.Trusted(st, c.paths.CACert),
			})
		}
	}
	c.profiles = profiles
	c.browsers = certs.RunningBrowsers()
	c.updated = time.Now()
	return c.profiles, c.browsers
}

// newScene returns an independent test scene driven by the real navigation
// model.
//
// The point of running nav.Step here rather than re-deriving the camera in
// JavaScript is diagnostic: if the built-in scene moves correctly but a CAD
// site does not, the fault is in the WAMP layer or the client; if the scene
// is wrong too, it is the device path or the navigation model. A lookalike in
// the page could not distinguish those.
func (u *uiState) newScene() webui.SceneStepper {
	return newSceneWith(u.live.Get().Nav(), u.motion)
}

// adoptNav records a navigation change the drive loop made itself, so a
// button toggle and the web UI do not end up describing different states.
func (u *uiState) adoptNav(n nav.Config) {
	u.live.Update(func(c *config.Config) {
		c.DominantAxis = n.DominantAxis
		c.EnableRotation = n.EnableRotation
		c.EnableTranslation = n.EnableTranslation
		if n.Mode == nav.ModeCamera {
			c.NavMode = "camera"
		} else {
			c.NavMode = "object"
		}
	})
}

// buttons is the live mapping, so a remap in the UI takes effect without a
// reconnect. A malformed entry is dropped rather than failing the frame:
// server.ParseButtons already rejected it when the settings were accepted.
func (u *uiState) buttons() map[int]server.ButtonAction {
	m, err := server.ParseButtons(u.live.Get().ButtonSpec())
	if err != nil {
		u.log.Warn("ignoring an unusable button mapping", "err", err)
		return nil
	}
	return m
}

// motion is the current deflection, or nothing when no device is open.
func (u *uiState) motion() spacenav.Motion {
	if u.device == nil {
		return spacenav.Motion{}
	}
	return u.device.State()
}

// newSceneWith is the testable core: it takes the navigation configuration and
// a source of deflection rather than reaching for a device.
func newSceneWith(cfg nav.Config, motion func() spacenav.Motion) webui.SceneStepper {
	// A unit model at the origin, viewed from four diagonal-lengths back, so
	// speeds expressed in model diagonals per second come out at a sensible
	// rate without any special-casing.
	const half = 0.5
	model := navlib.Box{-half, -half, -half, half, half, half}

	scene := nav.Scene{
		Camera:        navlib.Identity4(),
		Pivot:         [3]float64{0, 0, 0},
		ModelDiagonal: model.Diagonal(),
		ModelExtents:  model,
		Perspective:   true,
		Rotatable:     true,
	}
	// Back the camera off along its own +Z, which points out of the screen.
	scene.Camera = navlib.Translate([3]float64{0, 0, 4 * model.Diagonal()}).Mul(scene.Camera)

	return func(dt time.Duration) webui.SceneFrame {
		res := cfg.Step(motion(), dt, scene)
		scene.Camera = res.Camera

		// Copy: the caller marshals this while the next step is already
		// writing the scene's own matrix.
		out := make([]float64, 16)
		copy(out, scene.Camera[:])
		return webui.SceneFrame{Camera: out, Moved: res.Moved}
	}
}

// applySettings validates and applies a settings change from the web UI, and
// writes it to disk when asked.
//
// The incoming document is merged over the settings in force rather than
// parsed into a zero value, for the same reason Load does it: a page that
// sends only the field it changed must not silently clear everything else.
func (u *uiState) applySettings(body []byte, persist bool) error {
	current := u.live.Get()

	// Copy the map explicitly. Assigning the struct shares it, and
	// json.Unmarshal merges into an existing map rather than replacing it —
	// so decoding a rejected update would still have mutated the settings in
	// force, permanently, with no way for the user to see why every later
	// change then failed.
	incoming := current
	incoming.Buttons = make(map[string]string, len(current.Buttons))
	for id, action := range current.Buttons {
		incoming.Buttons[id] = action
	}

	if err := json.Unmarshal(body, &incoming); err != nil {
		return fmt.Errorf("that is not valid settings JSON: %w", err)
	}
	// Calibration is measured, not chosen. Live.Set restores it too; this
	// keeps the button mapping check below honest about what will be saved.
	incoming.CalibratedAt = current.CalibratedAt

	// One parser for button mappings, so the UI cannot accept a spelling the
	// drive loop would then reject every frame.
	if _, err := server.ParseButtons(incoming.ButtonSpec()); err != nil {
		return err
	}
	if err := u.live.Set(incoming); err != nil {
		return err
	}

	if persist {
		if err := config.Save(u.cfgPath, u.live.Get()); err != nil {
			return fmt.Errorf("applied, but saving failed: %w", err)
		}
		u.log.Info("settings saved", "path", u.cfgPath)
	}
	return nil
}
