package nav

import (
	"math"
	"testing"
	"time"

	"spacemouse-bridge/internal/navlib"
	"spacemouse-bridge/internal/spacenav"
)

func testConfig() Config {
	c := DefaultConfig()
	c.FullScale = 200
	c.Exponent = 1 // linear, so speeds are checkable by hand
	return c
}

// A camera at the origin looking down -Z, i.e. the identity pose.
func identityScene() Scene {
	return Scene{
		Camera:        navlib.Identity4(),
		ModelDiagonal: 10,
		Perspective:   true,
		Rotatable:     true,
	}
}

func almost(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestZeroInputLeavesCameraAlone(t *testing.T) {
	c := testConfig()
	s := identityScene()
	r := c.Step(spacenav.Motion{}, time.Second/60, s)
	if r.Moved {
		t.Error("zero input should not move the camera")
	}
	if r.Camera != s.Camera {
		t.Error("camera changed despite zero input")
	}
}

func TestDeadzoneRejectsRestingNoise(t *testing.T) {
	c := testConfig()
	s := identityScene()

	// The resting offset measured on a real Compact. With FullScale 200 and a
	// 6% deadzone, everything here is below 12 units and must be ignored, or
	// the view drifts while nobody touches the device.
	resting := spacenav.Motion{X: 6, Y: -2, Z: -11, RX: 9, RY: 4, RZ: -6}
	if r := c.Step(resting, time.Second/60, s); r.Moved {
		t.Errorf("resting noise %+v produced motion; deadzone is too small", resting)
	}
}

func TestDeadzoneRescalesWithoutJump(t *testing.T) {
	c := testConfig()
	// Just inside the deadzone yields zero; just outside yields a small value,
	// not an abrupt one.
	if v := c.shape(int32(c.Deadzone*c.FullScale) - 1); v != 0 {
		t.Errorf("inside deadzone = %v, want 0", v)
	}
	just := c.shape(int32(c.Deadzone*c.FullScale) + 2)
	if just <= 0 || just > 0.05 {
		t.Errorf("just outside deadzone = %v, want a small positive value", just)
	}
	if full := c.shape(int32(c.FullScale)); !almost(full, 1, 1e-9) {
		t.Errorf("full deflection = %v, want 1", full)
	}
}

func TestObjectModeMovesCameraOppositeTheCap(t *testing.T) {
	c := testConfig()
	s := identityScene()

	// Full push right. In object mode the model follows the cap, so the
	// camera must move LEFT.
	r := c.Step(spacenav.Motion{X: 200}, time.Second, s)
	if !r.Moved {
		t.Fatal("full deflection should move the camera")
	}
	tr := r.Camera.Translation()
	if tr[0] >= 0 {
		t.Errorf("camera x = %v, want negative (object mode moves opposite the cap)", tr[0])
	}
	// One second at full deflection travels TranslationSpeed model diagonals.
	want := c.TranslationSpeed * s.ModelDiagonal
	if !almost(math.Abs(tr[0]), want, 1e-9) {
		t.Errorf("distance = %v, want %v", math.Abs(tr[0]), want)
	}
	if !almost(tr[1], 0, 1e-9) || !almost(tr[2], 0, 1e-9) {
		t.Errorf("pure right push leaked into other axes: %v", tr)
	}
}

func TestCameraModeMovesWithTheCap(t *testing.T) {
	c := testConfig()
	c.Mode = ModeCamera
	r := c.Step(spacenav.Motion{X: 200}, time.Second, identityScene())
	if tr := r.Camera.Translation(); tr[0] <= 0 {
		t.Errorf("camera x = %v, want positive in camera mode", tr[0])
	}
}

func TestDeviceAxesMapToCameraFrame(t *testing.T) {
	c := testConfig()
	c.Mode = ModeCamera // makes the expected direction the same as the cap
	s := identityScene()

	cases := []struct {
		name  string
		in    spacenav.Motion
		axis  int
		wantP bool // want a positive component
	}{
		{"right is camera +X", spacenav.Motion{X: 200}, 0, true},
		{"up is camera +Y", spacenav.Motion{Z: 200}, 1, true},
		{"away is camera -Z", spacenav.Motion{Y: 200}, 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := c.Step(tc.in, time.Second, s)
			tr := r.Camera.Translation()
			v := tr[tc.axis]
			if tc.wantP && v <= 0 {
				t.Errorf("component %d = %v, want positive (got %v)", tc.axis, v, tr)
			}
			if !tc.wantP && v >= 0 {
				t.Errorf("component %d = %v, want negative (got %v)", tc.axis, v, tr)
			}
			for i, o := range tr {
				if i != tc.axis && !almost(o, 0, 1e-9) {
					t.Errorf("leaked into component %d: %v", i, tr)
				}
			}
		})
	}
}

func TestRotationPreservesDistanceFromPivot(t *testing.T) {
	c := testConfig()
	c.EnableTranslation = false

	// Camera 10 units back from a pivot at the origin.
	cam := navlib.Translate([3]float64{0, 0, 10})
	s := Scene{Camera: cam, Pivot: [3]float64{0, 0, 0}, ModelDiagonal: 10,
		Perspective: true, Rotatable: true}

	r := c.Step(spacenav.Motion{RZ: 200}, time.Second/4, s)
	if !r.Moved {
		t.Fatal("rotation input should move the camera")
	}
	tr := r.Camera.Translation()
	d := math.Sqrt(tr[0]*tr[0] + tr[1]*tr[1] + tr[2]*tr[2])
	if !almost(d, 10, 1e-9) {
		t.Errorf("distance from pivot = %v, want 10 (rotation must not translate)", d)
	}
	if almost(tr[0], 0, 1e-9) && almost(tr[1], 0, 1e-9) {
		t.Error("camera did not actually orbit")
	}
}

func TestRotationIsFrameRateIndependent(t *testing.T) {
	c := testConfig()
	c.EnableTranslation = false
	cam := navlib.Translate([3]float64{0, 0, 10})
	s := Scene{Camera: cam, ModelDiagonal: 10, Perspective: true, Rotatable: true}

	// One big step versus four small ones must land in the same place.
	one := c.Step(spacenav.Motion{RZ: 200}, 400*time.Millisecond, s)

	step := s
	for i := 0; i < 4; i++ {
		step.Camera = c.Step(spacenav.Motion{RZ: 200}, 100*time.Millisecond, step).Camera
	}

	for i := range one.Camera {
		if !almost(one.Camera[i], step.Camera[i], 1e-3) {
			t.Fatalf("element %d: one step %v vs four steps %v", i, one.Camera[i], step.Camera[i])
		}
	}
}

func TestDominantAxisSuppressesCrossTalk(t *testing.T) {
	c := testConfig()
	c.DominantAxis = true
	c.EnableRotation = false

	// A right push with heavy cross-talk, as a hurried gesture produces.
	a := c.shapeAll(spacenav.Motion{X: 200, Y: 140, RZ: 120})
	if a.x == 0 {
		t.Fatal("dominant axis dropped the winner")
	}
	if a.y != 0 || a.rz != 0 {
		t.Errorf("cross-talk survived: %+v", a)
	}
}

func TestCrossTalkSurvivesWithoutDominantAxis(t *testing.T) {
	c := testConfig()
	a := c.shapeAll(spacenav.Motion{X: 200, Y: 140})
	if a.y == 0 {
		t.Error("without DominantAxis, secondary axes should pass through")
	}
}

func TestZeroModelDiagonalStillMoves(t *testing.T) {
	c := testConfig()
	s := identityScene()
	s.ModelDiagonal = 0 // a client that reports nothing useful

	r := c.Step(spacenav.Motion{X: 200}, time.Second, s)
	if !r.Moved {
		t.Fatal("should still move with an unknown model size")
	}
	if tr := r.Camera.Translation(); almost(tr[0], 0, 1e-9) {
		t.Error("zero diagonal collapsed the motion to nothing")
	}
}

func orthoScene() Scene {
	return Scene{
		Camera:        navlib.Identity4(),
		ModelDiagonal: 10,
		Perspective:   false,
		Rotatable:     true,
		ViewExtents:   navlib.Box{-4, -3, -1000, 4, 3, 1000},
	}
}

func TestOrthoZoomScalesExtentsInsteadOfDollying(t *testing.T) {
	c := testConfig()
	s := orthoScene()

	// Push the cap away. In object mode the model recedes, so an orthographic
	// view must widen rather than dolly the camera.
	r := c.Step(spacenav.Motion{Y: 200}, time.Second/4, s)
	if !r.Moved {
		t.Fatal("push away should do something in an orthographic view")
	}
	if !r.ExtentsChanged {
		t.Fatal("orthographic zoom must scale view.extents")
	}
	if r.Extents.Diagonal() <= s.ViewExtents.Diagonal() {
		t.Errorf("extents shrank (%v -> %v); pushing away should widen the view",
			s.ViewExtents.Diagonal(), r.Extents.Diagonal())
	}
	// The camera must not have dollied along its forward axis.
	if tr := r.Camera.Translation(); !almost(tr[2], 0, 1e-9) {
		t.Errorf("camera dollied by %v in an orthographic view", tr[2])
	}
}

func TestOrthoZoomIsReversible(t *testing.T) {
	c := testConfig()
	s := orthoScene()
	before := s.ViewExtents

	in := c.Step(spacenav.Motion{Y: 200}, time.Second/3, s)
	s.ViewExtents = in.Extents
	out := c.Step(spacenav.Motion{Y: -200}, time.Second/3, s)

	// Exponential scaling means in-then-out returns exactly to the start.
	for i := range before {
		if !almost(out.Extents[i], before[i], 1e-9) {
			t.Fatalf("element %d: %v after a round trip, want %v", i, out.Extents[i], before[i])
		}
	}
}

func TestOrthoStillPans(t *testing.T) {
	c := testConfig()
	r := c.Step(spacenav.Motion{X: 200}, time.Second, orthoScene())
	if tr := r.Camera.Translation(); almost(tr[0], 0, 1e-9) {
		t.Error("orthographic views should still pan sideways")
	}
	if r.ExtentsChanged {
		t.Error("a sideways pan should not rescale the view")
	}
}

func TestPerspectiveDoesNotTouchExtents(t *testing.T) {
	c := testConfig()
	s := identityScene()
	s.ViewExtents = navlib.Box{-4, -3, -1000, 4, 3, 1000}

	r := c.Step(spacenav.Motion{Y: 200}, time.Second/4, s)
	if r.ExtentsChanged {
		t.Error("a perspective view should dolly, not rescale")
	}
	if tr := r.Camera.Translation(); almost(tr[2], 0, 1e-9) {
		t.Error("a perspective view should dolly the camera")
	}
}

func TestNonRotatableViewSuppressesRotation(t *testing.T) {
	c := testConfig()
	s := identityScene()
	s.Rotatable = false

	r := c.Step(spacenav.Motion{RZ: 200}, time.Second/4, s)
	if r.Moved {
		t.Error("rotation must be suppressed when the client pins the view")
	}
}

func TestEmptyExtentsFallsBackToDolly(t *testing.T) {
	c := testConfig()
	s := orthoScene()
	s.ViewExtents = navlib.Box{} // client did not supply one

	r := c.Step(spacenav.Motion{Y: 200}, time.Second/4, s)
	if r.ExtentsChanged {
		t.Error("cannot rescale an unknown view box")
	}
	if tr := r.Camera.Translation(); almost(tr[2], 0, 1e-9) {
		t.Error("with no view box, fall back to moving the camera")
	}
}

func fitScene() Scene {
	return Scene{
		Camera:       navlib.Translate([3]float64{0, 0, 3}), // far too close
		ModelExtents: navlib.Box{-1, -1, -1, 1, 1, 1},
		Perspective:  true,
		Rotatable:    true,
	}
}

func TestFitBacksOffToFrameTheModel(t *testing.T) {
	s := fitScene()
	r := Fit(s, 45*math.Pi/180)
	if !r.Moved {
		t.Fatal("fit should move the camera")
	}

	centre := s.ModelExtents.Center()
	tr := r.Camera.Translation()
	d := math.Sqrt(math.Pow(tr[0]-centre[0], 2) + math.Pow(tr[1]-centre[1], 2) + math.Pow(tr[2]-centre[2], 2))

	radius := s.ModelExtents.Diagonal() / 2
	want := radius / math.Sin(22.5*math.Pi/180) * FitPadding
	if !almost(d, want, 1e-6) {
		t.Errorf("distance = %v, want %v", d, want)
	}
	if d <= 3 {
		t.Errorf("fit did not back away from an over-close camera: %v", d)
	}
}

func TestFitKeepsOrientation(t *testing.T) {
	s := fitScene()
	// Look at the model from an angle rather than down an axis.
	s.Camera = navlib.RotateAxis([3]float64{0, 1, 0}, 0.7).Mul(navlib.Translate([3]float64{0, 0, 3}))

	r := Fit(s, 45*math.Pi/180)
	for col := 0; col < 3; col++ {
		for row := 0; row < 3; row++ {
			if !almost(r.Camera.At(row, col), s.Camera.At(row, col), 1e-9) {
				t.Fatalf("fit changed the orientation at (%d,%d)", row, col)
			}
		}
	}
}

func TestFitOrthographicResizesTheViewBox(t *testing.T) {
	s := fitScene()
	s.Perspective = false
	s.ViewExtents = navlib.Box{-40, -30, -1000, 40, 30, 1000} // 4:3, far too wide

	r := Fit(s, 0)
	if !r.ExtentsChanged {
		t.Fatal("an orthographic fit must resize the view box")
	}
	halfW := (r.Extents[3] - r.Extents[0]) / 2
	halfH := (r.Extents[4] - r.Extents[1]) / 2
	if !almost(halfW/halfH, 40.0/30.0, 1e-9) {
		t.Errorf("aspect = %v, want 4:3 preserved", halfW/halfH)
	}
	if halfH >= 30 {
		t.Errorf("view box did not shrink to frame the model: half-height %v", halfH)
	}
}

func TestFitWithoutModelExtentsDoesNothing(t *testing.T) {
	s := fitScene()
	s.ModelExtents = navlib.Box{}
	if r := Fit(s, 1); r.Moved {
		t.Error("fit should be a no-op when the model bounds are unknown")
	}
}
