package navlib

import "testing"

func TestNotifyFrameTimeNeverBlocks(t *testing.T) {
	c := &Controller{frameTimes: make(chan float64, 1)}

	// This runs on the WAMP read loop, which must never block: if it did, the
	// RPC replies the consumer is waiting for could not arrive.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			c.notifyFrameTime(float64(i))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("notifyFrameTime blocked with nobody reading")
	}
}

func TestFrameTimesKeepsTheNewest(t *testing.T) {
	c := &Controller{frameTimes: make(chan float64, 1)}
	c.notifyFrameTime(1)
	c.notifyFrameTime(2)
	c.notifyFrameTime(3)

	got := <-c.FrameTimes()
	if got != 3 {
		t.Errorf("got %v, want the newest timestamp (3); a stale frame time is worse than none", got)
	}
}

func TestClientDrivesFramesFlag(t *testing.T) {
	c := &Controller{frameTimes: make(chan float64, 1)}
	if c.ClientDrivesFrames() {
		t.Error("should default to false")
	}
	c.setClientDrivesFrames(true)
	if !c.ClientDrivesFrames() {
		t.Error("flag did not stick")
	}
}
