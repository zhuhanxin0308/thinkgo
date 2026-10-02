package health

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestAuditCompletedCheckReleasesSlotBeforeReturn(t *testing.T) {
	registry := NewRegistry()
	slots := registry.executionSlots()
	// Keep the other slots occupied, as if three noncooperative checks were running.
	for i := 0; i < cap(slots)-1; i++ {
		slots <- struct{}{}
	}
	check := func(context.Context) error { return nil }
	for i := 0; i < 100000; i++ {
		if err := registry.runCheckWithinBudget(check, context.Background()); err != nil {
			t.Fatalf("completed checks leaked a slot at iteration %d: %v", i, err)
		}
		if got := len(slots); got != cap(slots)-1 {
			t.Fatalf("result visible before slot release at iteration %d: occupied=%d", i, got)
		}
	}
}

func TestAuditFailedCheckReleasesSlotBeforeReturn(t *testing.T) {
	for _, check := range []Check{
		func(context.Context) error { return errors.New("dependency failed") },
		func(context.Context) error { panic("dependency panic") },
	} {
		registry := NewRegistry()
		if err := registry.runCheckWithinBudget(check, context.Background()); err == nil {
			t.Fatal("failed check was reported healthy")
		}
		if got := len(registry.executionSlots()); got != 0 {
			t.Fatalf("failed check retained %d slots after returning", got)
		}
	}
}

func TestAuditCheckGoexitReleasesSlot(t *testing.T) {
	registry := NewRegistry()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := registry.runCheckWithinBudget(func(context.Context) error {
		runtime.Goexit()
		return nil
	}, ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("check without a result must time out, got %v", err)
	}
	if got := len(registry.executionSlots()); got != 0 {
		t.Fatalf("Goexit retained %d slots", got)
	}
}
