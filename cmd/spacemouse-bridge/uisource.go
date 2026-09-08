package main

import (
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

	device   *spacenav.Client // nil outside drive mode
	settings config.Config
	paths    certs.Paths
	clients  *server.Clients

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
		Settings:  u.settings,
	}

	// The scales belong to the settings, not the device, so the page can draw
	// its axes to the right range even when nothing is connected.
	s.Device = webui.Device{
		FullScale:         u.settings.FullScale,
		RotationFullScale: u.settings.RotationFullScale,
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
	return newSceneWith(u.settings.Nav(), u.motion)
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
