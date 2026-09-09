package main

import (
	"testing"

	"github.com/kchellappan/spacemouse_linux_ws/internal/webui"
)

func validDevice() webui.DeviceConfig {
	return webui.DeviceConfig{
		Sensitivity:     1,
		AxisSensitivity: [6]float64{1, 1, 1, 1, 1, 1},
		Deadzone:        []int32{2, 2, 2, 2, 2, 2},
	}
}

func TestValidateDeviceConfigAcceptsTheDaemonDefaults(t *testing.T) {
	if err := validateDeviceConfig(validDevice()); err != nil {
		t.Errorf("spacenavd's own defaults were rejected: %v", err)
	}
}

// These values reach every application on the machine, so the bounds are
// about what would leave the device unusable rather than what is a strange
// choice. Zero sensitivity is the one that matters: spacenavd would then
// report nothing at all, everywhere, with no visible cause.
func TestValidateDeviceConfigRejectsUnusableValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(*webui.DeviceConfig)
	}{
		{"zero sensitivity", func(c *webui.DeviceConfig) { c.Sensitivity = 0 }},
		{"negative sensitivity", func(c *webui.DeviceConfig) { c.Sensitivity = -1 }},
		{"absurd sensitivity", func(c *webui.DeviceConfig) { c.Sensitivity = 1000 }},
		{"negative axis sensitivity", func(c *webui.DeviceConfig) { c.AxisSensitivity[2] = -1 }},
		{"too few dead zones", func(c *webui.DeviceConfig) { c.Deadzone = []int32{1, 2} }},
		{"too many dead zones", func(c *webui.DeviceConfig) { c.Deadzone = make([]int32, 9) }},
		{"negative dead zone", func(c *webui.DeviceConfig) { c.Deadzone[0] = -1 }},
		// Full deflection is 350 before sensitivity, so a dead zone near it
		// silently disables the axis in every application.
		{"dead zone past full scale", func(c *webui.DeviceConfig) { c.Deadzone[3] = 400 }},
	} {
		c := validDevice()
		tc.spoil(&c)
		if err := validateDeviceConfig(c); err == nil {
			t.Errorf("%s was accepted", tc.name)
		}
	}
}

// An axis sensitivity of zero is legitimate: it is how a user disables one
// axis without disabling the device.
func TestValidateDeviceConfigAllowsAZeroAxis(t *testing.T) {
	c := validDevice()
	c.AxisSensitivity[4] = 0
	if err := validateDeviceConfig(c); err != nil {
		t.Errorf("a disabled axis was rejected: %v", err)
	}
}
