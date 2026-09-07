package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kchellappan/spacemouse_linux_ws/internal/nav"
)

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing file is the ordinary state before first run, got %v", err)
	}
	if !reflect.DeepEqual(c, Default()) {
		t.Errorf("Load returned %+v, want the defaults %+v", c, Default())
	}
}

// The trap this guards: unmarshalling into a zero value would read every
// absent key as false or 0, so a file setting only fullScale would silently
// disable rotation and translation.
func TestPartialFileKeepsDefaultsForAbsentKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"fullScale": 216}`), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	d := Default()

	if c.FullScale != 216 {
		t.Errorf("fullScale = %v, want 216", c.FullScale)
	}
	if !c.EnableRotation {
		t.Error("rotation was disabled by a file that never mentioned it")
	}
	if !c.EnableTranslation {
		t.Error("translation was disabled by a file that never mentioned it")
	}
	if c.PanSpeed != d.PanSpeed || c.Curve != d.Curve || c.FrameRate != d.FrameRate {
		t.Errorf("absent numeric keys were zeroed: %+v", c)
	}
	if len(c.Buttons) != len(d.Buttons) {
		t.Errorf("buttons = %v, want the defaults %v", c.Buttons, d.Buttons)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")

	want := Default()
	want.FullScale = 216
	want.NoiseFloor = 6
	want.Deadzone = 0.08
	want.NavMode = "camera"
	want.EnableRotation = false
	want.Buttons = map[string]string{"0": "menu", "1": "fit"}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.FullScale != want.FullScale || got.NoiseFloor != want.NoiseFloor ||
		got.Deadzone != want.Deadzone || got.NavMode != want.NavMode ||
		got.EnableRotation != want.EnableRotation {
		t.Errorf("round trip changed the settings:\n got %+v\nwant %+v", got, want)
	}
	for id, action := range want.Buttons {
		if got.Buttons[id] != action {
			t.Errorf("button %s = %q, want %q", id, got.Buttons[id], action)
		}
	}
}

// Save writes every field because JSON has no comments: the file is the
// documentation. A partial write would leave a user guessing what is tunable.
func TestSaveWritesEveryField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Save wrote something that is not JSON: %v", err)
	}

	for _, key := range []string{
		"version", "navMode", "fullScale", "noiseFloor", "deadzone", "curve",
		"panSpeed", "rotateSpeed", "zoomSpeed", "dominantAxis",
		"enableTranslation", "enableRotation", "frameRate", "buttons",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("Save omitted %q", key)
		}
	}
	if got["version"] != float64(Version) {
		t.Errorf("version = %v, want %d", got["version"], Version)
	}
}

// A typo in a hand-edited file must not read as "reset everything": the user
// would lose their calibration and never be told.
func TestLoadRejectsACorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"fullScale": 216,,}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a malformed file loaded without error")
	}
}

func TestNavAppliesTheTuning(t *testing.T) {
	c := Default()
	c.FullScale = 216
	c.Curve = 2.0
	c.NavMode = "camera"
	c.EnableRotation = false

	n := c.Nav()
	if n.FullScale != 216 || n.Exponent != 2.0 {
		t.Errorf("nav config = %+v, want the tuned values", n)
	}
	if n.Mode != nav.ModeCamera {
		t.Error("navMode \"camera\" did not select ModeCamera")
	}
	if n.EnableRotation {
		t.Error("rotation stayed enabled")
	}
}

func TestNavModeDefaultsToObject(t *testing.T) {
	c := Default()
	c.NavMode = "object"
	if c.Nav().Mode != nav.ModeObject {
		t.Error("navMode \"object\" did not select ModeObject")
	}
}

func TestButtonSpecIsStableAndNumericallySorted(t *testing.T) {
	c := Default()
	c.Buttons = map[string]string{"10": "menu", "9": "fit", "1": "none"}

	got := c.ButtonSpec()
	want := "1=none,9=fit,10=menu"
	if got != want {
		t.Errorf("ButtonSpec() = %q, want %q", got, want)
	}
	if again := c.ButtonSpec(); again != got {
		t.Errorf("ButtonSpec() is not stable: %q then %q", got, again)
	}
}

func TestButtonSpecIsEmptyWhenNothingIsMapped(t *testing.T) {
	c := Default()
	c.Buttons = nil
	if got := c.ButtonSpec(); got != "" {
		t.Errorf("ButtonSpec() = %q, want empty", got)
	}
}
