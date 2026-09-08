package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

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

// A version 1 file had one fullScale covering both halves. Reading it as
// though rotation had no scale at all would leave rotation normalised against
// the built-in default while translation used the measured value.
func TestVersion1FileMigratesFullScaleToRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"version": 1, "fullScale": 146}`), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.FullScale != 146 {
		t.Errorf("fullScale = %v, want 146", c.FullScale)
	}
	if c.RotationFullScale != 146 {
		t.Errorf("rotationFullScale = %v, want the migrated 146", c.RotationFullScale)
	}
	if c.Version != Version {
		t.Errorf("version = %d, want %d after migration", c.Version, Version)
	}
}

// A current file must be left alone: migrating it would flatten a deliberate
// split back into one number.
func TestVersion2FileKeepsBothScales(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"version": 2, "fullScale": 216, "rotationFullScale": 146}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.FullScale != 216 || c.RotationFullScale != 146 {
		t.Errorf("scales = %v/%v, want 216/146", c.FullScale, c.RotationFullScale)
	}
}

func TestNavCarriesBothScales(t *testing.T) {
	c := Default()
	c.FullScale = 216
	c.RotationFullScale = 146

	n := c.Nav()
	if n.FullScale != 216 || n.RotationFullScale != 146 {
		t.Errorf("nav scales = %v/%v, want 216/146", n.FullScale, n.RotationFullScale)
	}
}

// A correctly calibrated SpaceMouse reports exactly the built-in 350, so
// whether calibration ran cannot be inferred from the values. It is recorded.
func TestCalibratedIsFalseUntilCalibrationWrites(t *testing.T) {
	if Default().Calibrated() {
		t.Error("the built-in defaults report themselves as calibrated")
	}

	c := Default()
	c.FullScale = 350 // the value a real calibration produces on a stock device
	if c.Calibrated() {
		t.Error("a device whose measured full scale equals the default reads as uncalibrated")
	}

	c.CalibratedAt = time.Now()
	if !c.Calibrated() {
		t.Error("a stamped configuration does not report as calibrated")
	}
}

// Version 2 files carry no calibratedAt. Calibration is the only writer, so
// the file existing is itself evidence it ran, and its mtime is when.
func TestVersion2FileTakesCalibrationTimeFromTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"version": 2, "fullScale": 350, "rotationFullScale": 310}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	want := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, want, want); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Calibrated() {
		t.Fatal("a version 2 file was treated as never calibrated")
	}
	if !c.CalibratedAt.Truncate(time.Second).Equal(want) {
		t.Errorf("calibratedAt = %v, want the file's mtime %v", c.CalibratedAt, want)
	}
	if c.Version != Version {
		t.Errorf("version = %d, want %d", c.Version, Version)
	}
}

// Missing file means nothing has ever been calibrated, and no mtime exists to
// borrow.
func TestNoFileMeansNotCalibrated(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Calibrated() {
		t.Error("an absent file reported as calibrated")
	}
}

// A current file states its own calibration time and must not have the file's
// mtime substituted, which would change every time anything rewrites it.
func TestVersion3FileKeepsItsOwnCalibrationTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	stamp := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

	want := Default()
	want.CalibratedAt = stamp
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CalibratedAt.Equal(stamp) {
		t.Errorf("calibratedAt = %v, want the recorded %v", got.CalibratedAt, stamp)
	}
}
