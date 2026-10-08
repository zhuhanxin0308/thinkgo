package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/event"
	frameworklog "github.com/zhuhanxin0308/thinkgo/v3/log"
)

type issue33HTTPSubscriber func(*event.Dispatcher) error

func (subscribe issue33HTTPSubscriber) Subscribe(d *event.Dispatcher) error { return subscribe(d) }

func issue33WarningCount(t *testing.T, logger *frameworklog.Log, driver *accessLogDriver) int {
	t.Helper()
	if err := logger.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range driver.allEntries() {
		if entry.Level == frameworklog.LevelWarning && entry.Message == applicationRunWarningMessage {
			count++
			if entry.Context["event"] != event.EventHttpRun || entry.Context["scope"] != "application" {
				t.Errorf("wrong warning context: %#v", entry.Context)
			}
		}
	}
	return count
}

func TestIssue33HTTPWarnsOnceBeforeRequests(t *testing.T) {
	for _, mode := range []string{"direct", "alias", "wildcard", "subscriber", "project_only", "unrelated"} {
		t.Run(mode, func(t *testing.T) {
			app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
			driver := &accessLogDriver{}
			logger := frameworklog.NewLog(driver)
			app.Instance(string(framework.ServiceLog), logger)
			d := event.NewDispatcher()
			app.Instance(string(framework.ServiceEvent), d)
			var projectCalls, appCalls, businessCalls atomic.Int64
			if err := d.Listen(event.EventHttpRun, &event.SimpleListener{Handler: func(event.Event) error { projectCalls.Add(1); return nil }}); err != nil {
				t.Fatal(err)
			}
			listener := &event.SimpleListener{Handler: func(current event.Event) error {
				if current.Name() == event.EventHttpRun {
					appCalls.Add(1)
				}
				return nil
			}}
			name := event.EventHttpRun
			switch mode {
			case "alias":
				name = "before-request"
				if err := d.BindEvent(name, func(interface{}) event.Event { return event.NewHttpRunEvent() }); err != nil {
					t.Fatal(err)
				}
			case "wildcard":
				name = "framework.*"
			case "unrelated":
				name = event.EventHttpEnd
			}
			if mode == "subscriber" {
				if err := d.SubscribeApplication(issue33HTTPSubscriber(func(staged *event.Dispatcher) error { return staged.Listen("HttpRun", listener, true) })); err != nil {
					t.Fatal(err)
				}
			} else if mode != "project_only" {
				if err := d.ListenApplicationEvents(map[string][]event.Listener{name: {listener}}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := mustHTTPRoute(t, app).Get("/scope", func(*fwcontext.Request) *fwcontext.Response {
				businessCalls.Add(1)
				return fwcontext.NewResponse().Content("ok")
			}); err != nil {
				t.Fatal(err)
			}
			handler := newTestHTTPHandler(t, app)
			if err := handler.ensureInitialized(); err != nil {
				t.Fatal(err)
			}
			want := 1
			if mode == "project_only" || mode == "unrelated" {
				want = 0
			}
			if got := issue33WarningCount(t, logger, driver); got != want {
				t.Fatalf("startup warnings=%d want=%d", got, want)
			}
			if err := handler.reloadRuntimeServices(); err != nil {
				t.Fatal(err)
			}
			for range 4 {
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://example.com/scope", nil))
				if recorder.Code != 200 || recorder.Body.String() != "ok" {
					t.Fatalf("response=%d %q", recorder.Code, recorder.Body.String())
				}
			}
			if got := issue33WarningCount(t, logger, driver); got != want {
				t.Fatalf("request-amplified warnings=%d want=%d", got, want)
			}
			if appCalls.Load() != 0 || projectCalls.Load() != 4 || businessCalls.Load() != 4 {
				t.Fatalf("dispatch changed: application=%d project=%d business=%d", appCalls.Load(), projectCalls.Load(), businessCalls.Load())
			}
		})
	}
}

func TestIssue33HTTPDoesNotWarnBeforeFinalInitialization(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	driver := &accessLogDriver{}
	logger := frameworklog.NewLog(driver)
	app.Instance(string(framework.ServiceLog), logger)
	d := event.NewDispatcher()
	app.Instance(string(framework.ServiceEvent), d)
	if err := d.ListenApplicationEvents(map[string][]event.Listener{"HttpRun": {&event.SimpleListener{Handler: func(event.Event) error { return nil }}}}); err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, app)
	if got := issue33WarningCount(t, logger, driver); got != 0 {
		t.Fatalf("warning before final services: %d", got)
	}
	if err := d.Remove("HttpRun"); err != nil {
		t.Fatal(err)
	}
	if err := handler.ensureInitialized(); err != nil {
		t.Fatal(err)
	}
	if got := issue33WarningCount(t, logger, driver); got != 0 {
		t.Fatalf("warning for removed registration: %d", got)
	}
}

func TestIssue33WarningConcurrentAndMissingDependencies(t *testing.T) {
	var empty *Http
	empty.warnApplicationRunListeners()
	d := event.NewDispatcher()
	handler := &Http{event: d}
	handler.warnApplicationRunListeners()
	if err := d.ListenApplicationEvents(map[string][]event.Listener{"HttpRun": {&event.SimpleListener{Handler: func(event.Event) error { return nil }}}}); err != nil {
		t.Fatal(err)
	}
	handler.warnApplicationRunListeners()
	driver := &accessLogDriver{}
	logger := frameworklog.NewLog(driver)
	t.Cleanup(func() {
		if err := logger.Close(); err != nil {
			t.Error(err)
		}
	})
	handler.log = logger
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); handler.warnApplicationRunListeners() }()
	}
	wg.Wait()
	if got := issue33WarningCount(t, logger, driver); got != 1 {
		t.Fatalf("concurrent warning count=%d", got)
	}
}

type issue33LogCaptureProvider struct{ driver *accessLogDriver }

func (*issue33LogCaptureProvider) Register(*framework.App) error { return nil }
func (provider *issue33LogCaptureProvider) Boot(app *framework.App) error {
	logger, err := framework.ResolveServiceAs[*frameworklog.Log](app, framework.ServiceLog)
	if err != nil {
		return err
	}
	return logger.AddDriver(provider.driver)
}

func TestIssue33NativeApplicationScopesRemainSeparated(t *testing.T) {
	base := t.TempDir()
	writeNativeHTTPConfig(t, base, `{"app_env":"test","default_app":"index","server":{"host":"127.0.0.1","port":8080},"compression":{"enable":false}}`)
	driver := &accessLogDriver{}
	var projectRun, appRun, appEnd atomic.Int64
	listener := func(counter *atomic.Int64) event.Listener {
		return &event.SimpleListener{Handler: func(event.Event) error { counter.Add(1); return nil }}
	}
	app := framework.NewConsoleAppUninitialized(base)
	t.Cleanup(func() { _ = app.Close() })
	if err := app.RegisterApplications(func(current *framework.App) error {
		if err := current.RegisterProvider(&issue33LogCaptureProvider{driver: driver}); err != nil {
			return err
		}
		return current.LoadEvent(framework.EventDefinition{Listen: map[string][]event.Listener{event.EventHttpRun: {listener(&projectRun)}}})
	}, framework.ApplicationDefinition{Name: "index", Register: func(current *framework.App) error {
		if err := current.LoadEvent(framework.EventDefinition{Listen: map[string][]event.Listener{event.EventHttpRun: {listener(&appRun)}, event.EventHttpEnd: {listener(&appEnd)}}}); err != nil {
			return err
		}
		return current.RegisterRouteLoader(func(current *framework.App) error {
			current.Route().Get("/", func() string { return "ok" })
			return nil
		})
	}}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHttp(app)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.ensureInitialized(); err != nil {
		t.Fatal(err)
	}
	logger := handler.applicationHost.applications["index"].log
	want := 0
	for _, kernel := range []*Http{handler, handler.applicationHost.applications["index"]} {
		if kernel.event.HasApplicationListener(event.EventHttpRun) {
			want++
		}
		if err := kernel.log.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	before := issue33WarningCount(t, logger, driver)
	if want < 1 || before != want {
		t.Fatalf("native startup warnings=%d", before)
	}
	for range 3 {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
		if r.Code != 200 || r.Body.String() != "ok" {
			t.Fatalf("native response=%d %q", r.Code, r.Body.String())
		}
	}
	if got := issue33WarningCount(t, logger, driver); got != before {
		t.Fatalf("native warning repeated: %d", got)
	}
	if projectRun.Load() != 3 || appRun.Load() != 0 || appEnd.Load() != 3 {
		t.Fatalf("native phases changed: projectRun=%d appRun=%d appEnd=%d", projectRun.Load(), appRun.Load(), appEnd.Load())
	}
}
