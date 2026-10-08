package http

import (
	"sync/atomic"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/event"
	frameworklog "github.com/zhuhanxin0308/thinkgo/v3/log"
)

// TestIssue33WarningUsesFinalReboundLogger reproduces the legal window between
// initialization and run-lease acquisition. The runtime logger must receive the
// diagnostic even when the provisional logger has already received it.
func TestIssue33WarningUsesFinalReboundLogger(t *testing.T) {
	for _, replaceEvents := range []bool{false, true} {
		name := "logger_only"
		if replaceEvents {
			name = "logger_and_dispatcher"
		}
		t.Run(name, func(t *testing.T) {
			app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
			oldDriver, finalDriver := &accessLogDriver{}, &accessLogDriver{}
			oldLog, finalLog := frameworklog.NewLog(oldDriver), frameworklog.NewLog(finalDriver)
			t.Cleanup(func() { _ = oldLog.Close(); _ = finalLog.Close() })
			var calls atomic.Int64
			listener := &event.SimpleListener{Handler: func(event.Event) error { calls.Add(1); return nil }}
			dispatcher := event.NewDispatcher()
			if err := dispatcher.ListenApplicationEvents(map[string][]event.Listener{"HttpRun": {listener}}); err != nil {
				t.Fatal(err)
			}
			if err := app.Instance(string(framework.ServiceLog), oldLog); err != nil {
				t.Fatal(err)
			}
			if err := app.Instance(string(framework.ServiceEvent), dispatcher); err != nil {
				t.Fatal(err)
			}
			handler := newTestHTTPHandler(t, app)
			if err := handler.ensureInitialized(); err != nil {
				t.Fatal(err)
			}
			if got := issue33WarningCount(t, oldLog, oldDriver); got != 1 {
				t.Fatalf("provisional warnings=%d, want 1", got)
			}

			if err := app.Instance(string(framework.ServiceLog), finalLog); err != nil {
				t.Fatal(err)
			}
			if replaceEvents {
				dispatcher = event.NewDispatcher()
				if err := dispatcher.ListenApplicationEvents(map[string][]event.Listener{"framework.*": {listener}}); err != nil {
					t.Fatal(err)
				}
				if err := app.Instance(string(framework.ServiceEvent), dispatcher); err != nil {
					t.Fatal(err)
				}
			}
			leases, err := handler.acquireApplicationRunLeases()
			if err != nil {
				t.Fatal(err)
			}
			defer leases.Release()
			for range 3 {
				if err := handler.reloadApplicationRuntimeServices(); err != nil {
					t.Fatal(err)
				}
			}
			if handler.log != finalLog || handler.event != dispatcher {
				t.Fatal("runtime services did not publish final snapshot")
			}
			if got := issue33WarningCount(t, finalLog, finalDriver); got != 1 {
				t.Fatalf("final logger warnings=%d, want 1", got)
			}
			if got := issue33WarningCount(t, oldLog, oldDriver); got != 1 {
				t.Fatalf("old logger received runtime warning: %d", got)
			}
			if got := calls.Load(); got != 0 {
				t.Fatalf("diagnostic executed application listeners: %d", got)
			}
		})
	}
}

func TestIssue33WarningTracksPublishedEventSnapshot(t *testing.T) {
	for _, finalHasListener := range []bool{false, true} {
		name := "removed_listener"
		if finalHasListener {
			name = "replacement_listener"
		}
		t.Run(name, func(t *testing.T) {
			app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
			driver := &accessLogDriver{}
			logger := frameworklog.NewLog(driver)
			t.Cleanup(func() { _ = logger.Close() })
			listener := &event.SimpleListener{Handler: func(event.Event) error { t.Error("diagnostic invoked listener"); return nil }}
			original, final := event.NewDispatcher(), event.NewDispatcher()
			if err := original.ListenApplicationEvents(map[string][]event.Listener{"HttpRun": {listener}}); err != nil {
				t.Fatal(err)
			}
			if finalHasListener {
				if err := final.ListenApplicationEvents(map[string][]event.Listener{"HttpRun": {listener}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := app.Instance(string(framework.ServiceLog), logger); err != nil {
				t.Fatal(err)
			}
			if err := app.Instance(string(framework.ServiceEvent), original); err != nil {
				t.Fatal(err)
			}
			handler := newTestHTTPHandler(t, app)
			if err := handler.ensureInitialized(); err != nil {
				t.Fatal(err)
			}
			if got := issue33WarningCount(t, logger, driver); got != 1 {
				t.Fatalf("initial warnings=%d", got)
			}
			if err := app.Instance(string(framework.ServiceEvent), final); err != nil {
				t.Fatal(err)
			}
			leases, err := handler.acquireApplicationRunLeases()
			if err != nil {
				t.Fatal(err)
			}
			defer leases.Release()
			for range 3 {
				if err := handler.reloadApplicationRuntimeServices(); err != nil {
					t.Fatal(err)
				}
			}
			want := 1
			if finalHasListener {
				want = 2
			}
			if got := issue33WarningCount(t, logger, driver); got != want {
				t.Fatalf("snapshot warnings=%d, want %d", got, want)
			}
		})
	}
}
