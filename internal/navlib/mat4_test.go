package navlib

import (
	"math"
	"testing"
)

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func almostVec(a, b [3]float64) bool {
	return almost(a[0], b[0]) && almost(a[1], b[1]) && almost(a[2], b[2])
}

func TestMulIdentity(t *testing.T) {
	m := Translate([3]float64{1, 2, 3})
	if got := m.Mul(Identity4()); got != m {
		t.Errorf("m*I = %v, want %v", got, m)
	}
	if got := Identity4().Mul(m); got != m {
		t.Errorf("I*m = %v, want %v", got, m)
	}
}

func TestTranslateMovesPoints(t *testing.T) {
	m := Translate([3]float64{1, 2, 3})
	if got := m.MulPoint([3]float64{10, 20, 30}); !almostVec(got, [3]float64{11, 22, 33}) {
		t.Errorf("MulPoint = %v, want [11 22 33]", got)
	}
	if got := m.Translation(); got != [3]float64{1, 2, 3} {
		t.Errorf("Translation = %v", got)
	}
}

func TestRotateAxisQuarterTurns(t *testing.T) {
	// +90 deg about Y takes +X to -Z (right-handed).
	r := RotateAxis([3]float64{0, 1, 0}, math.Pi/2)
	if got := r.MulPoint([3]float64{1, 0, 0}); !almostVec(got, [3]float64{0, 0, -1}) {
		t.Errorf("Ry(90) * +X = %v, want [0 0 -1]", got)
	}
	// +90 deg about Z takes +X to +Y.
	r = RotateAxis([3]float64{0, 0, 1}, math.Pi/2)
	if got := r.MulPoint([3]float64{1, 0, 0}); !almostVec(got, [3]float64{0, 1, 0}) {
		t.Errorf("Rz(90) * +X = %v, want [0 1 0]", got)
	}
}

func TestRotateAxisDegenerate(t *testing.T) {
	if got := RotateAxis([3]float64{0, 0, 0}, 1.0); got != Identity4() {
		t.Error("zero axis should yield identity")
	}
}

func TestOrbitAboutKeepsPivotFixed(t *testing.T) {
	pivot := [3]float64{5, 0, 0}
	m := OrbitAbout(pivot, RotateAxis([3]float64{0, 1, 0}, math.Pi/3))
	if got := m.MulPoint(pivot); !almostVec(got, pivot) {
		t.Errorf("pivot moved to %v, want %v", got, pivot)
	}
}

func TestOrbitAboutOriginMatchesPlainRotation(t *testing.T) {
	r := RotateAxis([3]float64{0, 1, 0}, 0.4)
	o := OrbitAbout([3]float64{0, 0, 0}, r)
	for i := range r {
		if !almost(r[i], o[i]) {
			t.Fatalf("orbit about origin differs at %d: %v vs %v", i, o[i], r[i])
		}
	}
}

func TestCanonicalRoundTripsBothLayouts(t *testing.T) {
	// Same logical transform expressed in each wire layout.
	col := []float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 1, 2, 3, 1}
	row := []float64{1, 0, 0, 1, 0, 1, 0, 2, 0, 0, 1, 3, 0, 0, 0, 1}

	cm, _ := NewMatrix4(col, LayoutColumnMajor)
	rm, _ := NewMatrix4(row, LayoutRowMajor)

	if cm.Canonical() != rm.Canonical() {
		t.Fatalf("layouts disagree after canonicalisation:\n col %v\n row %v",
			cm.Canonical(), rm.Canonical())
	}
	if got := cm.Canonical().Translation(); got != [3]float64{1, 2, 3} {
		t.Errorf("canonical translation = %v, want [1 2 3]", got)
	}

	// Round trip back out to each layout.
	if got := FromCanonical(cm.Canonical(), LayoutColumnMajor); got.M != cm.M {
		t.Errorf("column-major round trip: %v", got.M)
	}
	if got := FromCanonical(rm.Canonical(), LayoutRowMajor); got.M != rm.M {
		t.Errorf("row-major round trip: %v", got.M)
	}
}
