package main

import (
	"reflect"
	"testing"

	"github.com/kchellappan/spacemouse_linux_ws/internal/config"
)

// The precedence rule that matters: a flag left at its default must not
// overwrite a calibrated value. Comparing flag values against defaults would
// get this wrong for anyone who deliberately passes the default.
func TestUnsetFlagsDoNotOverwriteTheFile(t *testing.T) {
	c := config.Default()
	c.FullScale = 216
	c.Deadzone = 0.09
	c.EnableRotation = false

	// The struct carries every flag's current value, including defaults,
	// exactly as main does. None of them were set.
	applyFlagOverrides(&c, map[string]bool{}, flagValues{
		navMode: "object", fullScale: 350, deadzone: 0.06, curve: 1.6,
		panSpeed: 0.9, rotateSpeed: 1.6, zoomSpeed: 1.2, frameRate: 60,
		buttons: "0=fit,1=menu",
	})

	if c.FullScale != 216 {
		t.Errorf("fullScale = %v, want the file's 216", c.FullScale)
	}
	if c.Deadzone != 0.09 {
		t.Errorf("deadzone = %v, want the file's 0.09", c.Deadzone)
	}
	if c.EnableRotation {
		t.Error("rotation was re-enabled by an unset -no-rotate")
	}
}

func TestSetFlagsWin(t *testing.T) {
	c := config.Default()
	c.FullScale = 216
	c.NavMode = "object"

	applyFlagOverrides(&c, map[string]bool{"full-scale": true, "nav-mode": true},
		flagValues{fullScale: 300, navMode: "camera"})

	if c.FullScale != 300 {
		t.Errorf("fullScale = %v, want the flag's 300", c.FullScale)
	}
	if c.NavMode != "camera" {
		t.Errorf("navMode = %q, want the flag's camera", c.NavMode)
	}
}

// -no-rotate and -no-translate invert, which is the easiest pair to get
// backwards.
func TestNegatedFlagsInvert(t *testing.T) {
	c := config.Default()
	applyFlagOverrides(&c, map[string]bool{"no-rotate": true, "no-translate": true},
		flagValues{noRotate: true, noTranslate: true})

	if c.EnableRotation {
		t.Error("-no-rotate left rotation enabled")
	}
	if c.EnableTranslation {
		t.Error("-no-translate left translation enabled")
	}

	c = config.Default()
	c.EnableRotation = false
	applyFlagOverrides(&c, map[string]bool{"no-rotate": true}, flagValues{noRotate: false})
	if !c.EnableRotation {
		t.Error("-no-rotate=false did not re-enable rotation")
	}
}

func TestParseButtonSpec(t *testing.T) {
	for _, tc := range []struct {
		spec string
		want map[string]string
	}{
		{"0=fit,1=menu", map[string]string{"0": "fit", "1": "menu"}},
		{" 0 = fit , 1 = menu ", map[string]string{"0": "fit", "1": "menu"}},
		{"", map[string]string{}},
		{"0=none", map[string]string{"0": "none"}},
		// Malformed entries are dropped here; server.ParseButtons is the
		// authority that rejects them with a message.
		{"0=fit,garbage", map[string]string{"0": "fit"}},
	} {
		if got := parseButtonSpec(tc.spec); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseButtonSpec(%q) = %v, want %v", tc.spec, got, tc.want)
		}
	}
}

// The two halves have to agree, or a mapping set by flag would not survive
// being written to the file and read back.
func TestButtonSpecRoundTripsThroughTheConfig(t *testing.T) {
	c := config.Default()
	c.Buttons = parseButtonSpec("0=menu,1=fit")
	if got, want := c.ButtonSpec(), "0=menu,1=fit"; got != want {
		t.Errorf("round trip gave %q, want %q", got, want)
	}
}
