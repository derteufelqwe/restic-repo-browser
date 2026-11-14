package tasks

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOnlyOneTaskAtATime(t *testing.T) {
	m := NewManager()
	first, err := m.TryStart(KindSync, "Syncing a", 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.TryStart(KindCleanup, "Cleaning b", 2)
	var busy *BusyError
	if !errors.As(err, &busy) || busy.Current.Title != "Syncing a" {
		t.Fatalf("second start: %v", err)
	}
	first.Finish(nil)
	second, err := m.TryStart(KindCleanup, "Cleaning b", 2)
	if err != nil {
		t.Fatalf("start after finish: %v", err)
	}
	second.Finish(errors.New("boom"))
	if last := m.Last(); last == nil || last.Err != "boom" || last.Info.Title != "Cleaning b" {
		t.Errorf("last = %+v", last)
	}
	first.Finish(nil) // double finish is harmless
	if _, ok := m.Current(); ok {
		t.Error("slot should be free")
	}
}

func TestStartWaitsForFreeSlot(t *testing.T) {
	m := NewManager()
	first, _ := m.TryStart(KindSync, "first", 0)
	got := make(chan *Task, 1)
	go func() {
		tk, err := m.Start(context.Background(), KindCleanup, "second", 0)
		if err == nil {
			got <- tk
		}
	}()
	select {
	case <-got:
		t.Fatal("second task started while the first was running")
	case <-time.After(50 * time.Millisecond):
	}
	first.Finish(nil)
	select {
	case tk := <-got:
		tk.Finish(nil)
	case <-time.After(2 * time.Second):
		t.Fatal("second task never started")
	}
}

func TestStartHonoursContext(t *testing.T) {
	m := NewManager()
	m.TryStart(KindSync, "busy", 0)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := m.Start(ctx, KindSync, "x", 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
}
