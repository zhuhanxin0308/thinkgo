package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestAuditZeroValueRouter(t *testing.T) {
	var router Router
	called := false
	if err := router.Register("audit.task", HandlerFunc(func(context.Context, Task) error {
		called = true
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	task, err := NewTask("audit.task", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := router.HandleTask(context.Background(), task); err != nil || !called {
		t.Fatalf("dispatch: called=%t err=%v", called, err)
	}
	if err := router.Register("late.task", HandlerFunc(func(context.Context, Task) error { return nil })); !errors.Is(err, ErrRouterFrozen) {
		t.Fatalf("expected frozen router, got %v", err)
	}
}

func TestAuditZeroValueRouterConcurrentRegistration(t *testing.T) {
	var router Router
	const workers = 16
	var group sync.WaitGroup
	group.Add(workers)
	for index := 0; index < workers; index++ {
		go func(index int) {
			defer group.Done()
			err := router.Register(fmt.Sprintf("audit.%d", index), HandlerFunc(func(context.Context, Task) error { return nil }))
			if err != nil {
				t.Errorf("register: %v", err)
			}
		}(index)
	}
	group.Wait()
	for index := 0; index < workers; index++ {
		task, err := NewTask(fmt.Sprintf("audit.%d", index), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := router.HandleTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuditRouterPreservesDefensiveCopyAndValidation(t *testing.T) {
	router := NewRouter()
	task, err := NewTask("audit.task", []byte("payload"), map[string]string{"traceparent": "original"})
	if err != nil {
		t.Fatal(err)
	}
	handlerError := errors.New("handler failure")
	called := 0
	if err := router.Register(task.Type(), HandlerFunc(func(_ context.Context, received Task) error {
		called++
		// Even a same-package handler must receive an independent snapshot.
		received.payload[0] = 'X'
		received.headers["traceparent"] = "changed"
		return handlerError
	})); err != nil {
		t.Fatal(err)
	}
	if err := router.HandleTask(context.Background(), task); !errors.Is(err, handlerError) {
		t.Fatalf("handler error was not preserved: %v", err)
	}
	if string(task.Payload()) != "payload" || task.Headers()["traceparent"] != "original" {
		t.Fatal("dispatch aliased the original task")
	}
	for _, invalid := range []Task{
		{},
		{taskType: "audit.task", payload: make([]byte, MaximumPayloadBytes+1)},
		{taskType: "audit.task", headers: map[string]string{"bad name": "value"}},
	} {
		if err := router.HandleTask(context.Background(), invalid); !errors.Is(err, ErrInvalidTask) {
			t.Fatalf("invalid task was accepted: %v", err)
		}
	}
	if called != 1 {
		t.Fatalf("handler ran %d times, expected 1", called)
	}
}

func BenchmarkAuditRouterDispatch(b *testing.B) {
	for _, size := range []int{0, 1024, 64 << 10, MaximumPayloadBytes} {
		b.Run(fmt.Sprintf("payload_%d", size), func(b *testing.B) {
			task, err := NewTask("audit.task", make([]byte, size), map[string]string{"traceparent": "trace", "tenant": "test"})
			if err != nil {
				b.Fatal(err)
			}
			router := NewRouter()
			if err := router.Register(task.Type(), HandlerFunc(func(_ context.Context, received Task) error {
				if len(received.payload) != size {
					return ErrInvalidTask
				}
				return nil
			})); err != nil {
				b.Fatal(err)
			}
			ctx := context.Background()
			b.ReportAllocs()
			b.SetBytes(int64(size))
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				if err := router.HandleTask(ctx, task); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
