package logbuf

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func TestSinceReturnsRecordsInOrder(t *testing.T) {
	b := New(10)
	for i := 0; i < 3; i++ {
		b.Append(Record{Message: string(rune('a' + i))})
	}

	got := b.Since(0)
	if len(got) != 3 {
		t.Fatalf("got %d records, want 3", len(got))
	}
	for i, want := range []string{"a", "b", "c"} {
		if got[i].Message != want {
			t.Errorf("record %d = %q, want %q", i, got[i].Message, want)
		}
		if got[i].Seq != uint64(i+1) {
			t.Errorf("record %d seq = %d, want %d", i, got[i].Seq, i+1)
		}
	}
}

// A client resuming after a dropped connection asks for what it missed. It
// must not be handed records it already displayed.
func TestSinceExcludesWhatTheClientHas(t *testing.T) {
	b := New(10)
	for i := 0; i < 5; i++ {
		b.Append(Record{Message: "x"})
	}

	got := b.Since(3)
	if len(got) != 2 {
		t.Fatalf("got %d records after seq 3, want 2", len(got))
	}
	if got[0].Seq != 4 || got[1].Seq != 5 {
		t.Errorf("got seqs %d,%d, want 4,5", got[0].Seq, got[1].Seq)
	}
}

// The ring is fixed so a long-running service cannot grow without bound. The
// oldest records go, not the newest: recent history is what diagnoses a
// problem.
func TestRingDropsOldestWhenFull(t *testing.T) {
	b := New(3)
	for _, m := range []string{"a", "b", "c", "d", "e"} {
		b.Append(Record{Message: m})
	}

	got := b.Since(0)
	if len(got) != 3 {
		t.Fatalf("got %d records, want the ring size 3", len(got))
	}
	for i, want := range []string{"c", "d", "e"} {
		if got[i].Message != want {
			t.Errorf("record %d = %q, want %q", i, got[i].Message, want)
		}
	}
}

func TestSubscribeWakesOnWrite(t *testing.T) {
	b := New(10)
	ch, release := b.Subscribe()
	defer release()

	b.Append(Record{Message: "hello"})

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("no wake-up after a write")
	}
}

// Wake-ups coalesce on purpose: a subscriber that falls behind must miss
// notifications, never records, and must never make Append block.
func TestSubscribeCoalescesAndNeverBlocksTheWriter(t *testing.T) {
	b := New(100)
	ch, release := b.Subscribe()
	defer release()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			b.Append(Record{Message: "x"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Append blocked on a subscriber that was not reading")
	}

	if got := len(b.Since(0)); got != 50 {
		t.Errorf("buffer holds %d records, want all 50", got)
	}
	<-ch // one pending wake-up, not fifty
	select {
	case <-ch:
		t.Error("wake-ups did not coalesce")
	default:
	}
}

func TestReleaseStopsWakeUps(t *testing.T) {
	b := New(10)
	ch, release := b.Subscribe()
	release()

	b.Append(Record{Message: "x"})
	select {
	case <-ch:
		t.Error("a released subscriber was still woken")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHandlerCapturesRecordsAndAttributes(t *testing.T) {
	b := New(10)
	log := slog.New(NewHandler(slog.NewTextHandler(io.Discard, nil), b))

	log.Info("client connected", "origin", "https://cad.onshape.com")

	got := b.Since(0)
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0].Message != "client connected" {
		t.Errorf("message = %q", got[0].Message)
	}
	if got[0].Level != "INFO" {
		t.Errorf("level = %q, want INFO", got[0].Level)
	}
	if got[0].Attrs["origin"] != "https://cad.onshape.com" {
		t.Errorf("attrs = %v", got[0].Attrs)
	}
}

// slog carries WithAttrs values on the handler rather than the record, so a
// naive wrapper silently loses them.
func TestHandlerKeepsAttributesFromWith(t *testing.T) {
	b := New(10)
	log := slog.New(NewHandler(slog.NewTextHandler(io.Discard, nil), b)).With("component", "drive")

	log.Warn("stalled", "ms", 120)

	got := b.Since(0)
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0].Attrs["component"] != "drive" {
		t.Errorf("WithAttrs value lost: %v", got[0].Attrs)
	}
	if got[0].Attrs["ms"] != "120" {
		t.Errorf("record attr lost: %v", got[0].Attrs)
	}
}

// Every loop in the bridge logs, so the buffer is written from several
// goroutines at once.
func TestConcurrentAppendIsSafe(t *testing.T) {
	b := New(64)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				b.Append(Record{Message: "x"})
			}
		}()
	}
	wg.Wait()

	if got := len(b.Since(0)); got != 64 {
		t.Errorf("buffer holds %d records, want the ring size 64", got)
	}
}

func TestHandlerRespectsLevel(t *testing.T) {
	b := New(10)
	next := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})
	h := NewHandler(next, b)

	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("debug reported as enabled when the wrapped handler is at warn")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("error reported as disabled")
	}
}
