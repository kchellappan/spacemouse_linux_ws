// Package nav turns SpaceMouse deflection into camera motion.
//
// This is the part 3Dconnexion ships as a closed binary: the driver reads the
// application's camera and scene, computes a new pose, and writes it back
// (see docs/01-architecture-and-data-model.md). Everything here is a pure
// function of input plus scene, so it can be tested without a device or a
// browser — but the constants can only be judged by feel.
package nav

import (
	"math"
	"time"

	"spacemouse-bridge/internal/navlib"
	"spacemouse-bridge/internal/spacenav"
)

// Mode selects what the cap controls.
type Mode int

const (
	// ModeObject moves the model with the cap: push right, the model goes
	// right, so the camera moves left. This is the CAD default.
	ModeObject Mode = iota
	// ModeCamera moves the camera with the cap: push right, the camera goes
	// right and the model appears to move left.
	ModeCamera
)

// Config tunes the response. Speeds are expressed relative to the model so a
// large assembly and a small part both feel the same.
type Config struct {
	Mode Mode

	// FullScale is the device magnitude at full deflection, from -calibrate.
	FullScale float64

	// Deadzone is a fraction of full scale below which input is ignored.
	// It must exceed the device's resting noise floor or the view drifts
	// while nobody is touching it. See docs/05-spacenavd.md.
	Deadzone float64

	// Exponent shapes the response curve: 1 is linear, higher gives finer
	// control near centre while keeping full speed at the extremes.
	Exponent float64

	// TranslationSpeed is model diagonals per second at full deflection.
	TranslationSpeed float64
	// RotationSpeed is radians per second at full deflection.
	RotationSpeed float64
	// ZoomSpeed applies in orthographic views, where dollying the camera
	// changes nothing on screen and zoom must scale view.extents instead.
	// The scale is exponential so that zooming in and back out returns
	// exactly where it started, and so that it is frame-rate independent.
	// Units are e-foldings per second at full deflection.
	ZoomSpeed float64

	// DominantAxis suppresses every axis but the strongest, for users who
	// want each gesture to do exactly one thing. Off by default: cross-talk
	// on a SpaceMouse Compact turned out to depend on how deliberately the
	// cap is tipped rather than being inherent. See docs/05-spacenavd.md.
	DominantAxis bool

	// EnableTranslation and EnableRotation gate each half independently.
	EnableTranslation bool
	EnableRotation    bool
}

// DefaultConfig is a starting point, not a tuned one. FullScale should come
// from calibration; 350 is a typical SpaceMouse Compact reading.
func DefaultConfig() Config {
	return Config{
		Mode:              ModeObject,
		FullScale:         350,
		Deadzone:          0.06,
		Exponent:          1.6,
		TranslationSpeed:  0.9,
		RotationSpeed:     1.6,
		ZoomSpeed:         1.2,
		DominantAxis:      false,
		EnableTranslation: true,
		EnableRotation:    true,
	}
}

// Scene is what the client told us about its view and model.
type Scene struct {
	// Camera is the camera-to-world transform (navlib view.affine).
	Camera navlib.Mat4
	// Pivot is the world-space point rotation happens about.
	Pivot [3]float64
	// ModelDiagonal scales translation speed. Falling back to 1 gives
	// unit-per-second motion rather than nothing.
	ModelDiagonal float64
	// ModelExtents is the model bounding box, needed to frame the model on a
	// Fit. Optional for Step.
	ModelExtents navlib.Box

	// Perspective is the client's projection. In an orthographic view,
	// moving the camera along its forward axis changes nothing visually, so
	// zoom must scale ViewExtents instead.
	Perspective bool
	// ViewExtents is the current orthographic view box. Ignored when
	// Perspective is true, or when it is empty.
	ViewExtents navlib.Box
	// Rotatable is the client's view.rotatable. When false the view is
	// pinned — a 2D drafting view, say — and rotation must be suppressed.
	Rotatable bool
}

// Result is the computed pose. Extents is only meaningful when
// ExtentsChanged is set.
type Result struct {
	Camera         navlib.Mat4
	Extents        navlib.Box
	Moved          bool
	ExtentsChanged bool
}

// shape normalises one axis: clamp, deadzone with rescaling so there is no
// jump at the edge, then the response curve.
func (c Config) shape(v int32) float64 {
	if c.FullScale <= 0 {
		return 0
	}
	n := float64(v) / c.FullScale
	if n > 1 {
		n = 1
	} else if n < -1 {
		n = -1
	}

	a := math.Abs(n)
	if a <= c.Deadzone {
		return 0
	}
	if c.Deadzone < 1 {
		a = (a - c.Deadzone) / (1 - c.Deadzone)
	}
	if c.Exponent > 0 && c.Exponent != 1 {
		a = math.Pow(a, c.Exponent)
	}
	return math.Copysign(a, n)
}

// axes holds the six shaped inputs in device order.
type axes struct {
	x, y, z    float64 // translation: right, away, up
	rx, ry, rz float64 // rotation about those axes, right-handed
}

func (a axes) zero() bool {
	return a.x == 0 && a.y == 0 && a.z == 0 && a.rx == 0 && a.ry == 0 && a.rz == 0
}

// shapeAll converts raw device units into normalised, curved values.
func (c Config) shapeAll(m spacenav.Motion) axes {
	a := axes{
		x:  c.shape(m.X),
		y:  c.shape(m.Y),
		z:  c.shape(m.Z),
		rx: c.shape(m.RX),
		ry: c.shape(m.RY),
		rz: c.shape(m.RZ),
	}
	if c.DominantAxis {
		a = keepLargest(a)
	}
	return a
}

// keepLargest zeroes every axis but the strongest.
func keepLargest(a axes) axes {
	vals := []*float64{&a.x, &a.y, &a.z, &a.rx, &a.ry, &a.rz}
	var win *float64
	var best float64
	for _, v := range vals {
		if m := math.Abs(*v); m > best {
			best, win = m, v
		}
	}
	for _, v := range vals {
		if v != win {
			*v = 0
		}
	}
	return a
}

// ShapeIsZero reports whether the input falls entirely inside the deadzone.
//
// The drive loop calls this before reading anything from the client: when the
// user is not touching the device there is no reason to spend a round trip.
func (c Config) ShapeIsZero(m spacenav.Motion) bool {
	return c.shapeAll(m).zero()
}

// Step computes the next camera pose. Moved is false when the input is inside
// the deadzone and the camera should be left alone.
//
// Device axes are Z-up right-handed (X right, Y away, Z up) as established by
// -calibrate; the camera frame is OpenGL's (X right, Y up, -Z forward), which
// is what view.affine uses. The mapping between them is the (x, z, -y) swap
// below, and it is the single place that convention is encoded.
func (c Config) Step(m spacenav.Motion, dt time.Duration, s Scene) Result {
	out := Result{Camera: s.Camera}

	a := c.shapeAll(m)
	if a.zero() {
		return out
	}
	seconds := dt.Seconds()
	if seconds <= 0 {
		return out
	}

	diagonal := s.ModelDiagonal
	if diagonal <= 0 {
		diagonal = 1
	}

	// In object mode the model follows the cap, so the camera moves opposite.
	sign := -1.0
	if c.Mode == ModeCamera {
		sign = 1.0
	}

	if c.EnableRotation && s.Rotatable && (a.rx != 0 || a.ry != 0 || a.rz != 0) {
		// Device rotation axes into the camera frame.
		local := [3]float64{a.rx, a.rz, -a.ry}
		scale := sign * c.RotationSpeed * seconds
		local = [3]float64{local[0] * scale, local[1] * scale, local[2] * scale}

		if angle := math.Sqrt(local[0]*local[0] + local[1]*local[1] + local[2]*local[2]); angle > 0 {
			axisWorld := s.Camera.MulVec(local)
			r := navlib.RotateAxis(axisWorld, angle)
			out.Camera = navlib.OrbitAbout(s.Pivot, r).Mul(out.Camera)
			out.Moved = true
		}
	}

	if c.EnableTranslation && (a.x != 0 || a.y != 0 || a.z != 0) {
		// Device translation into the camera frame: right stays right, device
		// up becomes camera up, device "away" is camera -Z (into the screen).
		local := [3]float64{a.x, a.z, -a.y}
		scale := sign * c.TranslationSpeed * diagonal * seconds
		local = [3]float64{local[0] * scale, local[1] * scale, local[2] * scale}

		ortho := !s.Perspective && !s.ViewExtents.Empty()
		if ortho {
			// Dollying an orthographic camera is a no-op on screen; the
			// equivalent is scaling the view box. Positive camera-local Z is
			// backwards, away from the subject, so it zooms out.
			zoomIn := local[2]
			local[2] = 0
			if zoomIn != 0 {
				f := math.Exp(c.ZoomSpeed * sign * -zoomIn / (c.TranslationSpeed * diagonal))
				out.Extents = s.ViewExtents.Scaled(f)
				out.ExtentsChanged = true
				out.Moved = true
			}
		}

		if local[0] != 0 || local[1] != 0 || local[2] != 0 {
			world := s.Camera.MulVec(local)
			out.Camera = navlib.Translate(world).Mul(out.Camera)
			out.Moved = true
		}
	}

	return out
}

// FitPadding leaves a margin around the model so it does not touch the edges.
const FitPadding = 1.15

// Fit frames the model in the view, keeping the camera's current orientation.
//
// This is what a Fit button does. The navigation library performs it itself
// rather than asking the application to, so any client gets it for free.
//
// fov is the vertical field of view in radians; it is ignored for orthographic
// views, where the view box is resized instead of moving the camera.
func Fit(s Scene, fov float64) Result {
	out := Result{Camera: s.Camera}
	if s.ModelExtents.Empty() {
		return out
	}

	centre := s.ModelExtents.Center()
	radius := s.ModelExtents.Diagonal() / 2
	if radius <= 0 {
		return out
	}

	// The camera's own +Z points backwards, away from what it is looking at.
	back := s.Camera.MulVec([3]float64{0, 0, 1})
	if n := math.Sqrt(back[0]*back[0] + back[1]*back[1] + back[2]*back[2]); n > 0 {
		back = [3]float64{back[0] / n, back[1] / n, back[2] / n}
	} else {
		back = [3]float64{0, 0, 1}
	}

	var distance float64
	if s.Perspective {
		half := fov / 2
		if half <= 0 || half >= math.Pi/2 {
			half = 22.5 * math.Pi / 180 // a sane 45-degree default
		}
		// Distance at which a bounding sphere of this radius exactly fills
		// the vertical field of view.
		distance = radius / math.Sin(half) * FitPadding
	} else {
		// Orthographic: framing comes from the view box, so the camera only
		// needs to sit far enough back to keep the model inside near/far.
		distance = radius * 4

		if !s.ViewExtents.Empty() {
			halfW := (s.ViewExtents[3] - s.ViewExtents[0]) / 2
			halfH := (s.ViewExtents[4] - s.ViewExtents[1]) / 2
			aspect := 1.0
			if halfH > 0 {
				aspect = halfW / halfH
			}
			h := radius * FitPadding
			w := h * aspect
			out.Extents = navlib.Box{
				-w, -h, s.ViewExtents[2],
				w, h, s.ViewExtents[5],
			}
			out.ExtentsChanged = true
		}
	}

	pos := [3]float64{
		centre[0] + back[0]*distance,
		centre[1] + back[1]*distance,
		centre[2] + back[2]*distance,
	}

	cam := s.Camera
	cam[12], cam[13], cam[14] = pos[0], pos[1], pos[2]
	out.Camera = cam
	out.Moved = true
	return out
}
