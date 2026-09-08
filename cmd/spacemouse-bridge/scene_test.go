package main

import (
	"testing"
	"time"

	"github.com/kchellappan/spacemouse_linux_ws/internal/config"
	"github.com/kchellappan/spacemouse_linux_ws/internal/spacenav"
)

func steadyMotion(m spacenav.Motion) func() spacenav.Motion {
	return func() spacenav.Motion { return m }
}

// The scene starts framing the model rather than inside it, or the first
// thing a user sees is an empty canvas that looks like a broken page.
func TestSceneStartsBackedOffFromTheModel(t *testing.T) {
	step := newSceneWith(config.Default().Nav(), steadyMotion(spacenav.Motion{}))
	f := step(0)

	if len(f.Camera) != 16 {
		t.Fatalf("camera has %d elements, want 16", len(f.Camera))
	}
	// Column-major: translation sits at 12, 13, 14.
	if f.Camera[14] <= 1 {
		t.Errorf("camera Z = %v, want it backed away from a unit model", f.Camera[14])
	}
	if f.Camera[12] != 0 || f.Camera[13] != 0 {
		t.Errorf("camera starts off-axis at %v, %v", f.Camera[12], f.Camera[13])
	}
}

// A puck at rest must leave the view alone. Drift here would be visible as a
// slowly wandering cube and would send someone hunting a hardware fault.
func TestSceneDoesNotDriftAtRest(t *testing.T) {
	step := newSceneWith(config.Default().Nav(), steadyMotion(spacenav.Motion{}))
	before := step(0).Camera

	for i := 0; i < 120; i++ {
		step(16 * time.Millisecond)
	}
	after := step(0)

	if after.Moved {
		t.Error("the scene reported movement with the puck at rest")
	}
	for i := range before {
		if before[i] != after.Camera[i] {
			t.Fatalf("the camera drifted at rest: element %d went %v -> %v",
				i, before[i], after.Camera[i])
		}
	}
}

// The scene must actually be driven by the navigation model, not merely
// stream a constant — that is the whole diagnostic value of the page.
func TestSceneMovesWithDeflection(t *testing.T) {
	cfg := config.Default().Nav()
	step := newSceneWith(cfg, steadyMotion(spacenav.Motion{X: int32(cfg.FullScale)}))

	before := step(0).Camera
	var moved bool
	for i := 0; i < 30; i++ {
		if step(16 * time.Millisecond).Moved {
			moved = true
		}
	}
	after := step(0).Camera

	if !moved {
		t.Error("a full deflection produced no movement")
	}
	if before[12] == after[12] {
		t.Errorf("sliding right did not change the camera's X: still %v", after[12])
	}
}

// Each viewer gets an independent camera, so two tabs do not fight and a
// reload recentres — which is the entire reset mechanism, and it needs no
// mutating endpoint.
func TestEachSceneIsIndependent(t *testing.T) {
	cfg := config.Default().Nav()
	motion := steadyMotion(spacenav.Motion{X: int32(cfg.FullScale)})

	first := newSceneWith(cfg, motion)
	for i := 0; i < 30; i++ {
		first(16 * time.Millisecond)
	}
	moved := first(0).Camera

	fresh := newSceneWith(cfg, motion)(0).Camera
	if moved[12] == fresh[12] {
		t.Error("a new scene inherited the previous one's camera")
	}
	if fresh[12] != 0 {
		t.Errorf("a new scene did not start centred: X = %v", fresh[12])
	}
}

// Rotation is normalised against its own full scale, so a config with the two
// halves set apart must still drive the scene.
func TestSceneRotatesWithItsOwnScale(t *testing.T) {
	settings := config.Default()
	settings.FullScale = 350
	settings.RotationFullScale = 150
	cfg := settings.Nav()

	step := newSceneWith(cfg, steadyMotion(spacenav.Motion{RX: 150}))
	before := step(0).Camera
	for i := 0; i < 30; i++ {
		step(16 * time.Millisecond)
	}
	after := step(0).Camera

	same := true
	for i := range before {
		if before[i] != after[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("a full rotation deflection left the camera untouched")
	}
}
