package event

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

type issue33Subscriber func(*Dispatcher) error

func (subscribe issue33Subscriber) Subscribe(d *Dispatcher) error { return subscribe(d) }

func TestIssue33ApplicationListenerMatching(t *testing.T) {
	var nilDispatcher *Dispatcher
	var zero Dispatcher
	for _, d := range []*Dispatcher{nilDispatcher, &zero, NewDispatcher()} {
		for _, name := range []string{"", "bad\x00event", "*", EventHttpRun} {
			if d.HasApplicationListener(name) {
				t.Fatalf("empty dispatcher matched %q", name)
			}
		}
	}
	for _, name := range []string{EventHttpRun, "HttpRun", "before-request", "framework.*", "framework.Http?un", "framework.Http[RE]*"} {
		t.Run(name, func(t *testing.T) {
			d := NewDispatcher()
			if err := d.BindEvent("before-request", func(interface{}) Event { return NewHttpRunEvent() }); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int64
			listener := &SimpleListener{Handler: func(Event) error { calls.Add(1); return nil }}
			if err := d.Listen(EventHttpRun, listener); err != nil {
				t.Fatal(err)
			}
			if !d.HasListener(EventHttpRun) || d.HasApplicationListener(EventHttpRun) {
				t.Fatal("project listener classified as application")
			}
			if err := d.ListenApplicationEvents(map[string][]Listener{name: {listener}}); err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{EventHttpRun, "HttpRun", "before-request"} {
				if !d.HasApplicationListener(query) {
					t.Fatalf("missed application listener via %q", query)
				}
			}
			if calls.Load() != 0 {
				t.Fatal("query invoked a listener")
			}
			if err := d.DispatchProjectContext(context.Background(), NewHttpRunEvent()); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatalf("project dispatch invoked application listener: %d", calls.Load())
			}
		})
	}
	d := NewDispatcher()
	if err := d.ListenApplicationEvents(map[string][]Listener{EventHttpEnd: {&SimpleListener{Handler: func(Event) error { return nil }}}}); err != nil {
		t.Fatal(err)
	}
	if d.HasApplicationListener(EventHttpRun) || !d.HasApplicationListener(EventHttpEnd) {
		t.Fatal("unrelated event matched")
	}
}

func TestIssue33ApplicationSubscriberSnapshots(t *testing.T) {
	d := NewDispatcher()
	listener := &SimpleListener{Handler: func(Event) error { return nil }}
	var handle *ListenerHandle
	if err := d.SubscribeApplication(issue33Subscriber(func(staged *Dispatcher) error {
		var err error
		handle, err = staged.ListenWithHandle("framework.*", listener, true)
		return err
	})); err != nil {
		t.Fatal(err)
	}
	if !d.HasApplicationListener(EventHttpRun) {
		t.Fatal("subscriber first-listener not marked application")
	}
	if err := handle.Remove(); err != nil {
		t.Fatal(err)
	}
	if d.HasApplicationListener(EventHttpRun) {
		t.Fatal("removed handle survived cached lookup")
	}
	failure := errors.New("abort registration")
	if err := d.SubscribeApplication(issue33Subscriber(func(staged *Dispatcher) error {
		if err := staged.Listen("HttpRun", listener); err != nil {
			return err
		}
		return failure
	})); !errors.Is(err, failure) {
		t.Fatalf("subscriber error lost: %v", err)
	}
	if d.HasApplicationListener(EventHttpRun) {
		t.Fatal("failed subscriber leaked into query")
	}
	if err := d.RegisterTransaction(func(staged *Dispatcher) error {
		if err := staged.ListenApplicationEvents(map[string][]Listener{"HttpRun": {listener}}); err != nil {
			return err
		}
		return failure
	}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if d.HasApplicationListener(EventHttpRun) {
		t.Fatal("failed transaction leaked into query")
	}
	if err := d.ListenApplicationEvents(map[string][]Listener{"HttpRun": {listener}}); err != nil {
		t.Fatal(err)
	}
	if !d.HasApplicationListener(EventHttpRun) {
		t.Fatal("cached miss survived registration")
	}
	if err := d.Remove("HttpRun"); err != nil {
		t.Fatal(err)
	}
	if d.HasApplicationListener(EventHttpRun) {
		t.Fatal("cached match survived removal")
	}
}

func TestIssue33ApplicationListenerConcurrentQuery(t *testing.T) {
	d := NewDispatcher()
	listener := &SimpleListener{Handler: func(Event) error { return nil }}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				_ = d.HasApplicationListener("HttpRun")
			}
		}()
	}
	for range 50 {
		if err := d.ListenApplicationEvents(map[string][]Listener{"HttpRun": {listener}}); err != nil {
			t.Fatal(err)
		}
		if err := d.Remove("HttpRun"); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if d.HasApplicationListener("HttpRun") {
		t.Fatal("removed listeners remained visible")
	}
}
