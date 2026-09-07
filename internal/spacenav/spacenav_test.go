package spacenav

import (
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeDaemon serves one connection on a temp AF_UNIX socket, writing whatever
// frames the test queues.
func fakeDaemon(t *testing.T, frames [][8]int32) string {
	t.Helper()
	// Keep the path short: AF_UNIX paths are capped near 108 bytes.
	dir := t.TempDir()
	path := filepath.Join(dir, "s")

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, eventSize)
		for _, f := range frames {
			for i, v := range f {
				binary.LittleEndian.PutUint32(buf[i*4:i*4+4], uint32(v))
			}
			if _, err := conn.Write(buf); err != nil {
				return
			}
		}
		// Hold the connection open so the client does not see EOF mid-test.
		time.Sleep(2 * time.Second)
	}()
	return path
}

func TestDecodeMotionAxisOrder(t *testing.T) {
	// Wire slot order is (x, z, y) for BOTH triples. Verified against a real
	// SpaceMouse Compact via -calibrate: applying the swap to translation
	// only exchanges yaw with roll.
	ev := decode([8]int32{evMotion, 1, 2, 3, 4, 5, 6, 7})
	if ev.Motion == nil {
		t.Fatal("expected a motion event")
	}
	m := *ev.Motion
	if m.X != 1 || m.Z != 2 || m.Y != 3 {
		t.Errorf("translation = X%d Y%d Z%d, want X1 Y3 Z2", m.X, m.Y, m.Z)
	}
	if m.RX != 4 || m.RZ != 5 || m.RY != 6 {
		t.Errorf("rotation = RX%d RY%d RZ%d, want RX4 RY6 RZ5", m.RX, m.RY, m.RZ)
	}
	if m.Period != 7 {
		t.Errorf("period = %d, want 7", m.Period)
	}
}

// TestCalibratedAxisMap pins the mapping measured on a real SpaceMouse
// Compact, so the yaw/roll swap cannot silently return.
func TestCalibratedAxisMap(t *testing.T) {
	cases := []struct {
		gesture string
		slot    int
		value   int32
		want    func(Motion) int32
		wantVal int32
	}{
		{"slide right", 1, 200, func(m Motion) int32 { return m.X }, 200},
		{"lift up", 2, 200, func(m Motion) int32 { return m.Z }, 200},
		{"push away", 3, 200, func(m Motion) int32 { return m.Y }, 200},
		{"tip forward", 4, -200, func(m Motion) int32 { return m.RX }, -200},
		{"twist clockwise", 5, -200, func(m Motion) int32 { return m.RZ }, -200},
		{"tip right", 6, 200, func(m Motion) int32 { return m.RY }, 200},
	}
	for _, tc := range cases {
		t.Run(tc.gesture, func(t *testing.T) {
			var f [8]int32
			f[tc.slot] = tc.value
			m := *decode(f).Motion
			if got := tc.want(m); got != tc.wantVal {
				t.Errorf("%s: got %d, want %d (motion %+v)", tc.gesture, got, tc.wantVal, m)
			}
		})
	}
}

func TestDecodeButtons(t *testing.T) {
	ev := decode([8]int32{evButtonPress, 3})
	if ev.Button == nil || ev.Button.ID != 3 || !ev.Button.Pressed {
		t.Errorf("press decoded as %+v", ev.Button)
	}
	ev = decode([8]int32{evButtonRelease, 3})
	if ev.Button == nil || ev.Button.ID != 3 || ev.Button.Pressed {
		t.Errorf("release decoded as %+v", ev.Button)
	}
}

func TestMotionZero(t *testing.T) {
	if !(Motion{}).Zero() {
		t.Error("empty motion should be Zero")
	}
	if (Motion{Period: 12}).Zero() != true {
		t.Error("period alone should not make a motion non-Zero")
	}
	if (Motion{RZ: 1}).Zero() {
		t.Error("a non-zero axis should make Zero false")
	}
}

func TestClientStreamsAndLatches(t *testing.T) {
	path := fakeDaemon(t, [][8]int32{
		{evMotion, 10, 20, 30, 1, 2, 3, 16},
		{evButtonPress, 1},
		{evMotion, 0, 0, 0, 0, 0, 0, 16},
	})

	c, err := Dial(path, quiet())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	want := []string{"motion", "button", "motion"}
	for i, kind := range want {
		select {
		case ev := <-c.Events():
			switch kind {
			case "motion":
				if ev.Motion == nil {
					t.Fatalf("event %d: want motion, got %+v", i, ev)
				}
			case "button":
				if ev.Button == nil {
					t.Fatalf("event %d: want button, got %+v", i, ev)
				}
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}

	// The latch should hold the last motion, which re-centred.
	if got := c.State(); !got.Zero() {
		t.Errorf("State = %+v, want centred", got)
	}
}

func TestDialMissingSocket(t *testing.T) {
	_, err := Dial(filepath.Join(t.TempDir(), "absent"), quiet())
	if err == nil {
		t.Fatal("expected an error for a missing socket")
	}
}

func TestMotionDominant(t *testing.T) {
	// Values taken from a real SpaceMouse Compact trace: a left push with a
	// little roll cross-talk.
	m := Motion{X: -9, RY: 2, RZ: -4}
	d := m.Dominant()
	if d.Axis != AxisX {
		t.Errorf("dominant = %v, want x", d.Axis)
	}
	if d.Sign != -1 {
		t.Errorf("sign = %d, want -1", d.Sign)
	}
	if d.Peak != 9 || d.RunnerUp != 4 {
		t.Errorf("peak/runnerUp = %d/%d, want 9/4", d.Peak, d.RunnerUp)
	}
	if d.RunnerUpAxis != AxisRZ {
		t.Errorf("runner-up axis = %v, want rz", d.RunnerUpAxis)
	}
}

func TestDeflectionClean(t *testing.T) {
	if !(Deflection{Peak: 100, RunnerUp: 20}).Clean() {
		t.Error("100 vs 20 should be clean")
	}
	if (Deflection{Peak: 100, RunnerUp: 60}).Clean() {
		t.Error("100 vs 60 should not be clean")
	}
}

func TestMotionGetCoversEveryAxis(t *testing.T) {
	m := Motion{X: 1, Y: 2, Z: 3, RX: 4, RY: 5, RZ: 6}
	want := map[Axis]int32{AxisX: 1, AxisY: 2, AxisZ: 3, AxisRX: 4, AxisRY: 5, AxisRZ: 6}
	for a, w := range want {
		if got := m.Get(a); got != w {
			t.Errorf("Get(%v) = %d, want %d", a, got, w)
		}
	}
}

func TestDetectDeflectionFindsPeakAxis(t *testing.T) {
	// Ramp up on slot 6 (which is RY, not RZ — see TestCalibratedAxisMap),
	// with X as smaller cross-talk, then hold.
	var frames [][8]int32
	for _, v := range []int32{5, 20, 60, 90, 120, 120, 120, 120, 120, 120, 120, 120} {
		frames = append(frames, [8]int32{evMotion, v / 4, 0, 0, 0, 0, -v, 8})
	}
	path := fakeDaemon(t, frames)

	c, err := Dial(path, quiet())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	opts := DefaultDetectOptions
	opts.Settle = 30 * time.Millisecond

	// Give the reader a moment to latch the frames.
	time.Sleep(50 * time.Millisecond)
	d, err := DetectDeflection(t.Context(), c, opts, nil)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if d.Axis != AxisRY {
		t.Errorf("axis = %v, want ry", d.Axis)
	}
	if d.Sign != -1 {
		t.Errorf("sign = %d, want -1", d.Sign)
	}
	if !d.Clean() {
		t.Errorf("expected a clean deflection, got %v", d)
	}
}

func TestEmitDiscardsOldestNotNewest(t *testing.T) {
	// A consumer that never reads must still be able to observe the latest
	// state once it starts: a full buffer must not strand fresh events.
	c := &Client{
		events: make(chan Event, 4),
		done:   make(chan struct{}),
		log:    quiet(),
	}
	for i := int32(1); i <= 10; i++ {
		c.emit(Event{Motion: &Motion{X: i}})
	}

	var got []int32
	for len(c.events) > 0 {
		ev := <-c.events
		got = append(got, ev.Motion.X)
	}
	if len(got) != 4 {
		t.Fatalf("buffered %d events, want 4", len(got))
	}
	// The newest event must have survived.
	if got[len(got)-1] != 10 {
		t.Errorf("last buffered event = %d, want the newest (10); got %v", got[len(got)-1], got)
	}
	if got[0] == 1 {
		t.Errorf("oldest event was retained; got %v", got)
	}
	if c.State().X != 10 {
		t.Errorf("latch = %d, want 10", c.State().X)
	}
}

func TestWaitCentredUsesLatchNotBacklog(t *testing.T) {
	// Fill the channel with stale non-zero events, then centre the latch.
	// WaitCentred must notice immediately rather than draining the backlog.
	c := &Client{
		events: make(chan Event, 8),
		done:   make(chan struct{}),
		log:    quiet(),
	}
	for i := int32(1); i <= 8; i++ {
		c.emit(Event{Motion: &Motion{X: 100 + i}})
	}
	c.emit(Event{Motion: &Motion{}}) // re-centred

	if err := WaitCentred(t.Context(), c, time.Second, 0); err != nil {
		t.Fatalf("WaitCentred: %v", err)
	}
}

func TestAtRestToleratesResidualOffset(t *testing.T) {
	// Measured on a SpaceMouse Compact: the puck settles here, not at zero.
	resting := Motion{X: 6, Y: -2, Z: -11, RX: 9, RY: 4, RZ: -6}

	if resting.Zero() {
		t.Fatal("this sample is deliberately non-zero")
	}
	if resting.MaxMagnitude() != 11 {
		t.Errorf("MaxMagnitude = %d, want 11", resting.MaxMagnitude())
	}
	if !resting.AtRest(15) {
		t.Error("a residual offset of 11 should count as at rest under a 15 threshold")
	}
	if resting.AtRest(10) {
		t.Error("threshold 10 is below the offset; should not count as at rest")
	}
}

func TestWaitCentredAcceptsResidualOffset(t *testing.T) {
	c := &Client{events: make(chan Event, 4), done: make(chan struct{}), log: quiet()}
	c.emit(Event{Motion: &Motion{X: 6, Z: -11, RX: 9}})

	// Exact-zero semantics would hang here; the threshold must let it through.
	if err := WaitCentred(t.Context(), c, 300*time.Millisecond, 15); err != nil {
		t.Errorf("WaitCentred with threshold 15: %v", err)
	}
	if err := WaitCentred(t.Context(), c, 200*time.Millisecond, 5); err == nil {
		t.Error("threshold 5 is below the offset; expected a timeout")
	}
}

func TestMeasureNoiseFloor(t *testing.T) {
	c := &Client{events: make(chan Event, 4), done: make(chan struct{}), log: quiet()}
	c.emit(Event{Motion: &Motion{X: 3, Z: -7}})

	floor, err := MeasureNoiseFloor(t.Context(), c, 60*time.Millisecond)
	if err != nil {
		t.Fatalf("MeasureNoiseFloor: %v", err)
	}
	if floor != 7 {
		t.Errorf("floor = %d, want 7", floor)
	}
}

func TestDetectDeflectionTimesOut(t *testing.T) {
	// A daemon that only ever reports rest: detection must give up rather
	// than block, so a gesture the hardware cannot produce can be skipped.
	path := fakeDaemon(t, [][8]int32{{evMotion, 0, 0, 0, 0, 0, 0, 8}})
	c, err := Dial(path, quiet())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	opts := DefaultDetectOptions
	opts.DetectTimeout = 80 * time.Millisecond

	_, err = DetectDeflection(t.Context(), c, opts, nil)
	if !errors.Is(err, ErrNoDeflection) {
		t.Errorf("err = %v, want ErrNoDeflection", err)
	}
}

// hangUpDaemon accepts one connection and immediately drops it, the way
// spacenavd would if it were restarted underneath us.
func hangUpDaemon(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "s")

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		conn.Close()
	}()
	return path
}

// Losing spacenavd has to be observable. Navigation reads the latch, which
// keeps returning its last value forever, so without this signal a dead
// daemon looks exactly like a puck sitting still.
func TestDeadFiresWhenTheDaemonHangsUp(t *testing.T) {
	c, err := Dial(hangUpDaemon(t), quiet())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	select {
	case <-c.Dead():
	case <-time.After(2 * time.Second):
		t.Fatal("Dead was not closed after the daemon hung up")
	}
	if c.Err() == nil {
		t.Error("Err is nil after the daemon hung up; the caller cannot tell this from a clean Close")
	}
}

func TestDeadReportsNoErrorAfterClose(t *testing.T) {
	c, err := Dial(fakeDaemon(t, nil), quiet())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case <-c.Dead():
	case <-time.After(2 * time.Second):
		t.Fatal("Dead was not closed after Close")
	}
	if err := c.Err(); err != nil {
		t.Errorf("Err reports %v after a deliberate Close, want nil", err)
	}
}
