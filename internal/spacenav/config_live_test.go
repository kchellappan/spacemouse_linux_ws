package spacenav

import (
	"os"
	"testing"
)

// dialReal connects to a spacenavd actually running on this machine, and
// skips when there is none. CI installs the daemon, so this exercises the
// real protocol rather than only the fake one.
func dialReal(t *testing.T) *Client {
	t.Helper()
	var found string
	for _, p := range SocketPaths {
		if _, err := os.Stat(p); err == nil {
			found = p
			break
		}
	}
	if found == "" {
		t.Skip("no spacenavd socket on this machine")
	}
	c, err := Dial(found, quiet())
	if err != nil {
		t.Skipf("spacenavd socket present but not connectable: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// Negotiation has to succeed against a real daemon, or every configuration
// call silently reports "unsupported" and the feature looks broken rather
// than absent.
func TestLiveProtocolNegotiation(t *testing.T) {
	c := dialReal(t)
	t.Logf("negotiated protocol version %d", c.proto)

	if !c.ConfigSupported() {
		t.Skipf("daemon negotiated protocol %d; configuration needs 1 or above", c.proto)
	}
}

// Reading is safe: it changes nothing. Doing it against the real daemon is
// what proves the opcode values, which were transcribed by counting enum
// positions and cannot be verified any other way.
func TestLiveReadConfig(t *testing.T) {
	c := dialReal(t)
	if !c.ConfigSupported() {
		t.Skip("daemon does not support configuration requests")
	}

	// REQ_DEV_NAXES is unimplemented on spacenavd 1.2, so this is reported
	// rather than required. If it starts working, the probe in ReadConfig
	// can be replaced by it.
	if n, err := c.NumAxes(); err != nil {
		t.Logf("NumAxes unsupported on this daemon: %v", err)
	} else {
		t.Logf("device reports %d axes", n)
	}

	cfg, err := c.ReadConfig()
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	t.Logf("sensitivity=%v axis=%v swapYZ=%v deadzone=%v",
		cfg.Sensitivity, cfg.AxisSensitivity, cfg.SwapYZ, cfg.Deadzone)

	// A wrong opcode would most likely return a wildly implausible float
	// rather than an error, so sanity-check the shape of what came back.
	if cfg.Sensitivity <= 0 || cfg.Sensitivity > 100 {
		t.Errorf("global sensitivity = %v, which suggests the opcode is wrong", cfg.Sensitivity)
	}
	for i, s := range cfg.AxisSensitivity {
		if s <= 0 || s > 100 {
			t.Errorf("axis %d sensitivity = %v, which suggests the opcode is wrong", i, s)
		}
	}
	if len(cfg.Deadzone) == 0 {
		t.Error("no dead zones were discovered")
	}
	for i, dz := range cfg.Deadzone {
		if dz < 0 || dz > 1000 {
			t.Errorf("axis %d dead zone = %d, which is not plausible", i, dz)
		}
	}
}

// Writing is the half that can break other applications, so this proves the
// path without changing anything: it reads the daemon's settings and writes
// exactly those back. A wrong opcode still fails; a correct one is a no-op.
func TestLiveWriteConfigIsANoOpWhenNothingChanges(t *testing.T) {
	c := dialReal(t)
	if !c.ConfigSupported() {
		t.Skip("daemon does not support configuration requests")
	}

	before, err := c.ReadConfig()
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if err := c.WriteConfig(before); err != nil {
		t.Fatalf("writing back the current settings failed: %v", err)
	}

	after, err := c.ReadConfig()
	if err != nil {
		t.Fatalf("re-reading: %v", err)
	}
	if after.Sensitivity != before.Sensitivity || after.SwapYZ != before.SwapYZ {
		t.Errorf("a no-op write changed the settings: %+v -> %+v", before, after)
	}
	for i := range before.AxisSensitivity {
		if after.AxisSensitivity[i] != before.AxisSensitivity[i] {
			t.Errorf("axis %d sensitivity changed: %v -> %v",
				i, before.AxisSensitivity[i], after.AxisSensitivity[i])
		}
	}
	for i := range before.Deadzone {
		if after.Deadzone[i] != before.Deadzone[i] {
			t.Errorf("axis %d dead zone changed: %v -> %v",
				i, before.Deadzone[i], after.Deadzone[i])
		}
	}
}
