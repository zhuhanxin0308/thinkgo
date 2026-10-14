//go:build integration

package asynq

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	backend "github.com/hibiken/asynq"

	"github.com/zhuhanxin0308/thinkgo/v3/queue"
)

// TestLiveRedisQueueIntegrityWireContract exercises authenticated task delivery
// through real Redis. The shared replay oracle is independent of Redis but is
// an in-memory test model, not a deployable durable replay store.
func TestLiveRedisQueueIntegrityWireContract(t *testing.T) {
	live := newLiveQueue(t)
	store := &integrityTestStore{}
	signing, verification := integrityPolicies(t, store)
	producer, err := NewSignedProducer(live.config, signing)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := producer.Close(); err != nil {
			t.Errorf("close signed producer: %v", err)
		}
	})
	task, err := queue.NewTask("integration.integrity", []byte("live-integrity-payload"), map[string]string{"traceparent": "live-integrity-trace"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), liveQueueIOTimeout)
	info, err := producer.Enqueue(ctx, task, queue.EnqueueOptions{
		Queue: live.name, DisableRetry: true, Timeout: time.Minute, Retention: liveQueueRetention,
	})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	stored, err := live.inspector.GetTaskInfo(live.name, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	var envelope signedEnvelope
	if err := json.Unmarshal([]byte(stored.Headers[integrityHeader]), &envelope); err != nil {
		t.Fatalf("signature metadata did not survive Redis persistence: %v", err)
	}
	if len(info.ID) != 64 || envelope.Metadata.ID != info.ID || envelope.Metadata.Queue != live.name ||
		stored.ID != info.ID || stored.Queue != live.name || stored.Type != task.Type() ||
		string(stored.Payload) != string(task.Payload()) || stored.Headers["traceparent"] != "live-integrity-trace" || len(stored.Headers) != 2 {
		t.Fatalf("signed wire identity, payload or headers changed: %#v", stored)
	}
	var calls atomic.Int32
	processed := make(chan queue.Task, 4)
	startWorker := func() *Worker {
		t.Helper()
		worker, err := NewVerifiedWorker(live.config, WorkerConfig{
			Concurrency: 1, Queues: map[string]int{live.name: 1},
			ShutdownTimeout: liveQueueShutdown, TaskCheckInterval: liveQueuePollPeriod,
		}, verification)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := worker.Shutdown(); err != nil {
				t.Errorf("close verified worker: %v", err)
			}
		})
		if err := worker.Start(queue.HandlerFunc(func(ctx context.Context, delivered queue.Task) error {
			calls.Add(1)
			id, hasID := backend.GetTaskID(ctx)
			name, hasQueue := backend.GetQueueName(ctx)
			if !hasID || id != info.ID || !hasQueue || name != live.name {
				t.Errorf("handler received changed task identity: id=%q queue=%q", id, name)
			}
			processed <- delivered
			return nil
		})); err != nil {
			t.Fatal(err)
		}
		return worker
	}
	first := startWorker()
	completed := live.waitState(t, info.ID, backend.TaskStateCompleted)
	select {
	case delivered := <-processed:
		if delivered.Type() != task.Type() || string(delivered.Payload()) != string(task.Payload()) ||
			len(delivered.Headers()) != 1 || delivered.Headers()["traceparent"] != "live-integrity-trace" {
			t.Fatalf("verified handler received changed data or internal metadata: %#v", delivered)
		}
	default:
		t.Fatal("completed task never reached verified handler")
	}
	if calls.Load() != 1 || completed.ID != info.ID || completed.CompletedAt.IsZero() || completed.Retried != 0 {
		t.Fatalf("signed delivery did not complete once: calls=%d task=%#v", calls.Load(), completed)
	}
	if err := first.Shutdown(); err != nil {
		t.Fatal(err)
	}
	// Replace the worker and remove Redis's completed record. Redis can now accept
	// the same signed ID again; only the independently shared replay oracle rejects it.
	startWorker()
	if err := live.inspector.DeleteTask(live.name, info.ID); err != nil {
		t.Fatal(err)
	}
	enqueueWire := func(t *testing.T, id string, payload []byte, headers map[string]string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), liveQueueIOTimeout)
		defer cancel()
		_, err := live.producer.client.EnqueueContext(ctx, backend.NewTaskWithHeaders(stored.Type, payload, headers),
			backend.TaskID(id), backend.Queue(live.name), backend.MaxRetry(0), backend.Timeout(time.Minute), backend.Retention(liveQueueRetention))
		if err != nil {
			t.Fatal(err)
		}
	}
	enqueueWire(t, info.ID, stored.Payload, stored.Headers)
	replayed := live.waitState(t, info.ID, backend.TaskStateArchived)
	if !strings.Contains(replayed.LastErr, ErrTaskReplay.Error()) || calls.Load() != 1 {
		t.Fatalf("completed replay reached handler or lost rejection: calls=%d error=%q", calls.Load(), replayed.LastErr)
	}
	previousID := info.ID
	for _, mutation := range []string{"payload", "header", "id"} {
		t.Run(mutation, func(t *testing.T) {
			if err := live.inspector.DeleteTask(live.name, previousID); err != nil {
				t.Fatal(err)
			}
			id, payload := info.ID, stored.Payload
			headers := make(map[string]string, len(stored.Headers))
			for name, value := range stored.Headers {
				headers[name] = value
			}
			switch mutation {
			case "payload":
				payload = []byte("forged payload")
			case "header":
				headers["traceparent"] = "forged trace"
			case "id":
				id = "0" + info.ID[1:]
				if id == info.ID {
					id = "1" + info.ID[1:]
				}
			}
			enqueueWire(t, id, payload, headers)
			archived := live.waitState(t, id, backend.TaskStateArchived)
			if !strings.Contains(archived.LastErr, ErrTaskUnauthenticated.Error()) || calls.Load() != 1 {
				t.Fatalf("tampered %s reached handler or lost rejection: calls=%d error=%q", mutation, calls.Load(), archived.LastErr)
			}
			previousID = id
		})
	}
	store.mu.Lock()
	record := store.records[info.ID]
	terminal := record != nil && record.done && !record.active && record.attempts == 1
	store.mu.Unlock()
	if !terminal {
		t.Fatal("replay or tampering changed the independent completed identity")
	}
}
