package asynq

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	backend "github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/zhuhanxin0308/thinkgo/v3/queue"
)

// This is an oracle for the ReplayStore contract, not a deployable durable store.
type integrityTestStore struct {
	mu        sync.Mutex
	records   map[string]*integrityTestRecord
	claimErr  error
	finishErr error
	nilLease  bool
}

type integrityTestRecord struct {
	identity   TaskIdentity
	attempts   int
	active     bool
	done       bool
	generation uint64
}

type integrityTestLease struct {
	store      *integrityTestStore
	record     *integrityTestRecord
	generation uint64
	finished   bool
}

func (store *integrityTestStore) Claim(ctx context.Context, identity TaskIdentity) (TaskLease, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.claimErr != nil {
		return nil, store.claimErr
	}
	if store.nilLease {
		return nil, nil
	}
	if store.records == nil {
		store.records = map[string]*integrityTestRecord{}
	}
	record := store.records[identity.ID]
	if record == nil {
		record = &integrityTestRecord{identity: identity}
		store.records[identity.ID] = record
	}
	if record.identity != identity || record.active || record.done || record.attempts >= identity.MaxAttempts || !time.Now().Before(identity.ExpiresAt) {
		return nil, ErrTaskReplay
	}
	record.attempts++
	record.active = true
	record.generation++
	return &integrityTestLease{store: store, record: record, generation: record.generation}, nil
}

func (lease *integrityTestLease) Finish(ctx context.Context, success bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lease.store.mu.Lock()
	defer lease.store.mu.Unlock()
	if lease.finished {
		return nil
	}
	if lease.record.generation != lease.generation {
		return ErrTaskReplay
	}
	if lease.store.finishErr != nil {
		return lease.store.finishErr
	}
	lease.record.active = false
	lease.record.done = success
	lease.finished = true
	return nil
}

func integrityPolicies(t *testing.T, store ReplayStore) (*SigningPolicy, *VerificationPolicy) {
	t.Helper()
	key := []byte(strings.Repeat("k", 32))
	signing, err := NewSigningPolicy(SigningConfig{KeyID: "key-1", Key: key, Audience: "billing.production", ValidFor: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	verification, err := NewVerificationPolicy(VerificationConfig{
		Keys: map[string][]byte{"key-1": key}, Audience: "billing.production", MaxValidFor: time.Hour, Replay: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	return signing, verification
}

func TestIntegrityAuthenticatesEveryTaskFieldAndWindow(t *testing.T) {
	signing, verification := integrityPolicies(t, &integrityTestStore{})
	now := time.Unix(1_800_000_000, 0)
	task, _ := queue.NewTask("billing.charge", []byte("protected bytes"), map[string]string{"traceparent": "trace"})
	options := queue.EnqueueOptions{Queue: "billing", Timeout: time.Minute, MaxRetry: 2}
	newTask := func() (*backend.Task, signedEnvelope) {
		t.Helper()
		wire, _, err := signing.task(task, options, now)
		if err != nil {
			t.Fatal(err)
		}
		var envelope signedEnvelope
		if err := json.Unmarshal([]byte(wire.Headers()[integrityHeader]), &envelope); err != nil {
			t.Fatal(err)
		}
		return wire, envelope
	}
	wire, original := newTask()
	converted, _, err := verification.verify(wire, now)
	if err != nil || converted.Type() != task.Type() || string(converted.Payload()) != string(task.Payload()) || converted.Headers()["traceparent"] != "trace" || len(converted.Headers()) != 1 {
		t.Fatalf("authenticated task was not restored: %#v %v", converted, err)
	}
	for _, field := range []string{"type", "payload", "header", "version", "key", "audience", "id", "queue", "issued", "not-before", "expires", "retry", "timeout", "mac", "missing", "extra-json", "oversized", "oversized-payload", "oversized-headers"} {
		t.Run(field, func(t *testing.T) {
			wire, envelope := newTask()
			headers, payload, kind := wire.Headers(), wire.Payload(), wire.Type()
			switch field {
			case "type":
				kind = "billing.refund"
			case "payload":
				payload = []byte("changed")
			case "header":
				headers["traceparent"] = "changed"
			case "version":
				envelope.Metadata.Version++
			case "key":
				envelope.Metadata.KeyID = "unknown"
			case "audience":
				envelope.Metadata.Audience = "other.production"
			case "id":
				envelope.Metadata.ID = strings.Repeat("1", 64)
			case "queue":
				envelope.Metadata.Queue = "other"
			case "issued":
				envelope.Metadata.Issued--
			case "not-before":
				envelope.Metadata.NotBefore--
			case "expires":
				envelope.Metadata.Expires++
			case "retry":
				envelope.Metadata.MaxRetry++
			case "timeout":
				envelope.Metadata.Timeout++
			case "mac":
				envelope.MAC = "zz"
			case "oversized-payload":
				payload = make([]byte, queue.MaximumPayloadBytes+1)
			case "oversized-headers":
				for i := 0; i <= queue.MaximumHeaders; i++ {
					headers[strings.Repeat("x", i+1)] = "v"
				}
			}
			encoded, _ := json.Marshal(envelope)
			headers[integrityHeader] = string(encoded)
			switch field {
			case "missing":
				delete(headers, integrityHeader)
			case "extra-json":
				headers[integrityHeader] += "{}"
			case "oversized":
				headers[integrityHeader] = strings.Repeat("x", 2049)
			}
			if _, _, err := verification.verify(backend.NewTaskWithHeaders(kind, payload, headers), now); !errors.Is(err, ErrTaskUnauthenticated) {
				t.Fatalf("tampered %s admitted: %v", field, err)
			}
		})
	}
	for _, at := range []time.Time{now.Add(-time.Second), now.Add(time.Hour)} {
		if _, _, err := verification.verify(wire, at); !errors.Is(err, ErrTaskUnauthenticated) {
			t.Fatalf("invalid execution time admitted: %v", err)
		}
	}
	if _, _, err := verification.verify(nil, now); !errors.Is(err, ErrTaskUnauthenticated) {
		t.Fatal(err)
	}
	// A trusted signer cannot extend the verifier's local maximum lifetime.
	verification.config.MaxValidFor = time.Minute
	if _, _, err := verification.verify(wire, now); !errors.Is(err, ErrTaskUnauthenticated) {
		t.Fatal("verifier lifetime ignored")
	}
	verification.config.MaxValidFor = time.Hour
	// Verify expiry, ID, timeout and header syntax even with a valid MAC.
	for _, mutate := range []func(*signedMetadata){
		func(m *signedMetadata) { m.ID = "invalid" },
		func(m *signedMetadata) { m.ID = strings.Repeat("z", 64) },
		func(m *signedMetadata) { m.Timeout = 0 },
		func(m *signedMetadata) { m.MaxRetry = -1 },
		func(m *signedMetadata) { m.NotBefore = m.Expires },
	} {
		metadata := original.Metadata
		mutate(&metadata)
		headers := task.Headers()
		encoded, _ := json.Marshal(signedEnvelope{metadata, fmtMAC(metadata, task.Payload(), headers, signing.config.Key)})
		headers[integrityHeader] = string(encoded)
		if _, _, err := verification.verify(backend.NewTaskWithHeaders(task.Type(), task.Payload(), headers), now); !errors.Is(err, ErrTaskUnauthenticated) {
			t.Fatal("invalid signed bounds admitted")
		}
	}
	options.ProcessAt = now.Add(time.Minute)
	future, _, err := signing.task(task, options, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := verification.verify(future, now); !errors.Is(err, ErrTaskUnauthenticated) {
		t.Fatal("scheduled task ran early")
	}
	if _, _, err := verification.verify(future, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Protocol timestamps preserve fractional seconds for expiry and schedules.
	short, err := NewSigningPolicy(SigningConfig{KeyID: "key-1", Key: signing.config.Key, Audience: signing.config.Audience, ValidFor: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	fractional := now.Add(900 * time.Millisecond)
	shortTask, _, err := short.task(task, queue.EnqueueOptions{Timeout: 500 * time.Millisecond}, fractional)
	if err != nil {
		t.Fatal(err)
	}
	_, metadata, err := verification.verify(shortTask, fractional.Add(500*time.Millisecond))
	if err != nil || metadata.Expires != fractional.Add(time.Second).UnixNano() {
		t.Fatalf("fractional expiry truncated: %v", err)
	}
	scheduled, _, err := short.task(task, queue.EnqueueOptions{Timeout: 100 * time.Millisecond, ProcessAt: fractional.Add(50 * time.Millisecond)}, fractional)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := verification.verify(scheduled, fractional); !errors.Is(err, ErrTaskUnauthenticated) {
		t.Fatal("fractional schedule ran early")
	}
	_, scheduledMetadata, err := verification.verify(scheduled, now.Add(time.Second))
	if err != nil || scheduledMetadata.NotBefore != now.Add(time.Second).UnixNano() {
		t.Fatalf("rounded schedule not signed: %v", err)
	}
	if _, _, err := short.task(task, queue.EnqueueOptions{Timeout: 500 * time.Millisecond, ProcessAt: now.Add(200 * time.Millisecond)}, now.Add(100*time.Millisecond)); !errors.Is(err, queue.ErrInvalidOptions) {
		t.Fatal("rounded schedule exceeds execution window")
	}
}

func fmtMAC(metadata signedMetadata, payload []byte, headers map[string]string, key []byte) string {
	return hex.EncodeToString(integrityMAC(metadata, payload, headers, key))
}

func TestIntegrityConfigurationRotationAndSigningBoundaries(t *testing.T) {
	store := &integrityTestStore{}
	signing, verification := integrityPolicies(t, store)
	task, _ := queue.NewTask("charge", nil, nil)
	now := time.Now()
	old, _, err := signing.task(task, queue.EnqueueOptions{}, now)
	if err != nil {
		t.Fatal(err)
	}
	rotatedKey := []byte(strings.Repeat("n", 32))
	keys := map[string][]byte{"key-1": bytesKey(signing.config.Key), "key-2": rotatedKey}
	rotated, err := NewVerificationPolicy(VerificationConfig{Keys: keys, Audience: signing.config.Audience, MaxValidFor: time.Hour, Replay: store})
	if err != nil {
		t.Fatal(err)
	}
	keys["key-1"][0] = 'x'
	rotatedKey[0] = 'x'
	delete(keys, "key-1")
	if _, _, err := rotated.verify(old, now); err != nil {
		t.Fatalf("old key lost during rotation/snapshot: %v", err)
	}
	newSigning, err := NewSigningPolicy(SigningConfig{KeyID: "key-2", Key: rotated.config.Keys["key-2"], Audience: signing.config.Audience, ValidFor: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	newWire, _, err := newSigning.task(task, queue.EnqueueOptions{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := rotated.verify(newWire, now); err != nil {
		t.Fatal(err)
	}
	delete(rotated.config.Keys, "key-1")
	if _, _, err := rotated.verify(old, now); !errors.Is(err, ErrTaskUnauthenticated) {
		t.Fatal("retired key accepted")
	}
	for _, config := range []SigningConfig{{}, {KeyID: "bad space", Key: make([]byte, 32), Audience: "a", ValidFor: time.Hour}, {KeyID: "k", Key: make([]byte, 31), Audience: "a", ValidFor: time.Hour}} {
		if _, err := NewSigningPolicy(config); !errors.Is(err, queue.ErrInvalidOptions) {
			t.Fatal("invalid signing config accepted")
		}
	}
	var nilStore *integrityTestStore
	for _, config := range []VerificationConfig{{}, {Keys: map[string][]byte{"k": make([]byte, 32)}, Audience: "a", MaxValidFor: time.Hour, Replay: nilStore}, {Keys: map[string][]byte{"bad space": make([]byte, 32)}, Audience: "a", MaxValidFor: time.Hour, Replay: store}} {
		if _, err := NewVerificationPolicy(config); !errors.Is(err, queue.ErrInvalidOptions) {
			t.Fatal("invalid verifier accepted")
		}
	}
	for _, options := range []queue.EnqueueOptions{{Timeout: time.Hour}, {Queue: "invalid space"}, {ProcessAt: now.Add(time.Hour)}} {
		if _, _, err := signing.task(task, options, now); !errors.Is(err, queue.ErrInvalidOptions) {
			t.Fatal("invalid execution bounds admitted")
		}
	}
	if _, _, err := signing.task(queue.Task{}, queue.EnqueueOptions{}, now); !errors.Is(err, queue.ErrInvalidTask) {
		t.Fatal(err)
	}
	reserved, _ := queue.NewTask("charge", nil, map[string]string{strings.ToUpper(integrityHeader): "forged"})
	if _, _, err := signing.task(reserved, queue.EnqueueOptions{}, now); !errors.Is(err, queue.ErrInvalidTask) {
		t.Fatal(err)
	}
	// Config key is copied, and signed headers are absent from the original task.
	key := make([]byte, 32)
	copySigning, _ := NewSigningPolicy(SigningConfig{KeyID: "k", Key: key, Audience: "a", ValidFor: time.Hour})
	key[0] = 1
	if copySigning.config.Key[0] != 0 || len(task.Headers()) != 0 {
		t.Fatal("configuration/task mutated")
	}
	if err := verification.handle(context.Background(), old, queue.HandlerFunc(func(context.Context, queue.Task) error { t.Fatal("missing backend identity admitted"); return nil })); !errors.Is(err, backend.SkipRetry) {
		t.Fatal(err)
	}
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	for _, policy := range []*SigningPolicy{nil, {}} {
		if _, err := NewSignedProducer(RedisConfig{}, policy); !errors.Is(err, queue.ErrInvalidOptions) {
			t.Fatal(err)
		}
		if _, err := NewSignedProducerFromRedisClient(client, policy); !errors.Is(err, queue.ErrInvalidOptions) {
			t.Fatal(err)
		}
	}
	for _, policy := range []*VerificationPolicy{nil, {}} {
		if _, err := NewVerifiedWorker(RedisConfig{}, WorkerConfig{}, policy); !errors.Is(err, queue.ErrInvalidOptions) {
			t.Fatal(err)
		}
		if _, err := NewVerifiedWorkerFromRedisClient(client, WorkerConfig{}, policy); !errors.Is(err, queue.ErrInvalidOptions) {
			t.Fatal(err)
		}
	}
	producer, err := NewSignedProducer(RedisConfig{Address: server.Addr()}, signing)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	if _, err := producer.Enqueue(t.Context(), task, queue.EnqueueOptions{Timeout: time.Hour}); !errors.Is(err, queue.ErrInvalidOptions) {
		t.Fatal(err)
	}
	// The native schedule and signed NotBefore must agree after second rounding.
	schedule := time.Now().Add(2*time.Second + 100*time.Millisecond)
	info, err := producer.Enqueue(t.Context(), task, queue.EnqueueOptions{ProcessAt: schedule, Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	inspector := backend.NewInspectorFromRedisClient(client)
	stored, err := inspector.GetTaskInfo(info.Queue, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantSchedule := schedule.Truncate(time.Second).Add(time.Second)
	if !stored.NextProcessAt.Equal(wantSchedule) || stored.Timeout != time.Second {
		t.Fatalf("native scheduling/timeout precision changed bounds: %s %v", stored.NextProcessAt, stored.Timeout)
	}
	_, scheduledMetadata, err := verification.verify(backend.NewTaskWithHeaders(stored.Type, stored.Payload, stored.Headers), wantSchedule)
	if err != nil || scheduledMetadata.NotBefore != wantSchedule.UnixNano() || scheduledMetadata.Timeout != int64(500*time.Millisecond) {
		t.Fatalf("signed/native bounds differ: %#v %v", scheduledMetadata, err)
	}
	worker, err := NewVerifiedWorker(RedisConfig{Address: server.Addr()}, WorkerConfig{Concurrency: 1, Queues: map[string]int{"default": 1}, ShutdownTimeout: time.Second, TaskCheckInterval: 50 * time.Millisecond}, verification)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Shutdown()
	if _, err := NewSignedProducer(RedisConfig{}, signing); err == nil {
		t.Fatal("invalid redis accepted")
	}
	if _, err := NewSignedProducerFromRedisClient(nil, signing); err == nil {
		t.Fatal("nil redis accepted")
	}
	if _, err := NewVerifiedWorker(RedisConfig{}, WorkerConfig{}, verification); err == nil {
		t.Fatal("invalid redis accepted")
	}
	if _, err := NewVerifiedWorkerFromRedisClient(nil, WorkerConfig{}, verification); err == nil {
		t.Fatal("nil redis accepted")
	}
}

func bytesKey(key []byte) []byte { return append([]byte(nil), key...) }

func TestIntegrityRejectsNonUTF8HeadersBeforeCanonicalEncoding(t *testing.T) {
	signing, verification := integrityPolicies(t, &integrityTestStore{})
	now := time.Now()
	// U+FFFD is valid Unicode and must remain distinct from invalid UTF-8 bytes.
	task, err := queue.NewTask("charge", []byte{0xff}, map[string]string{"trace": "�"})
	if err != nil {
		t.Fatal(err)
	}
	wire, _, err := signing.task(task, queue.EnqueueOptions{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := verification.verify(wire, now); err != nil {
		t.Fatal(err)
	}
	// An invalid-byte task must never enter signing. A legacy/malformed signer
	// could otherwise give 0xff and 0xfe identical canonical JSON bytes.
	malformed, err := queue.NewTask("charge", nil, map[string]string{"trace": string([]byte{0xff})})
	if err != nil {
		t.Fatal(err)
	}
	metadata := signedMetadata{Version: 1, KeyID: signing.config.KeyID, Audience: signing.config.Audience,
		ID: strings.Repeat("a", 64), Type: malformed.Type(), Queue: queue.DefaultQueueName,
		Issued: now.UnixNano(), NotBefore: now.UnixNano(), Expires: now.Add(time.Hour).UnixNano(),
		MaxRetry: queue.DefaultMaxRetries, Timeout: int64(time.Minute)}
	headers := malformed.Headers()
	encoded, err := json.Marshal(signedEnvelope{metadata, fmtMAC(metadata, malformed.Payload(), headers, signing.config.Key)})
	if err != nil {
		t.Fatal(err)
	}
	headers[integrityHeader] = string(encoded)
	headers["trace"] = string([]byte{0xfe})
	if _, _, err := verification.verify(backend.NewTaskWithHeaders(malformed.Type(), malformed.Payload(), headers), now); !errors.Is(err, ErrTaskUnauthenticated) {
		t.Fatalf("distinct invalid UTF-8 bytes authenticated identically: %v", err)
	}
	for _, value := range []string{string([]byte{0xff}), string([]byte{0xfe}), string([]byte{0xc0, 0xaf})} {
		headers := wire.Headers()
		headers["trace"] = value
		if _, _, err := verification.verify(backend.NewTaskWithHeaders(wire.Type(), wire.Payload(), headers), now); !errors.Is(err, ErrTaskUnauthenticated) {
			t.Fatalf("invalid UTF-8 header reached authentication: err=%v", err)
		}
		// The ordinary queue API remains compatible; only authenticated encoding is stricter.
		invalid, err := queue.NewTask("charge", nil, map[string]string{"trace": value})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := signing.task(invalid, queue.EnqueueOptions{}, now); !errors.Is(err, queue.ErrInvalidTask) {
			t.Fatalf("signer admitted invalid UTF-8 header: %v", err)
		}
	}
}

func TestVerifiedWorkerRejectsTamperingAndSuccessfulReplay(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	store := &integrityTestStore{}
	signing, verification := integrityPolicies(t, store)
	worker, err := NewVerifiedWorkerFromRedisClient(client, WorkerConfig{Concurrency: 1, Queues: map[string]int{"default": 1}, ShutdownTimeout: time.Second, TaskCheckInterval: 50 * time.Millisecond}, verification)
	if err != nil {
		t.Fatal(err)
	}
	processed := make(chan queue.Task, 8)
	release := make(chan struct{})
	releaseHandler := sync.OnceFunc(func() { close(release) })
	if err := worker.Start(queue.HandlerFunc(func(ctx context.Context, task queue.Task) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Minute {
			t.Error("authenticated timeout not enforced")
		}
		processed <- task
		<-release
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	defer worker.Shutdown()
	// Release the handler before Shutdown on every assertion-failure path.
	defer releaseHandler()
	producer, err := NewSignedProducerFromRedisClient(client, signing)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	task, _ := queue.NewTask("charge", []byte("protected"), map[string]string{"traceparent": "trace"})
	options := queue.EnqueueOptions{DisableRetry: true, Timeout: time.Minute, UniqueFor: time.Minute, Retention: time.Hour}
	info, err := producer.Enqueue(t.Context(), task, options)
	if err != nil {
		t.Fatal(err)
	}
	inspector := backend.NewInspectorFromRedisClient(client)
	waitIntegrityState(t, inspector, info.ID, backend.TaskStateActive)
	// Asynq releases uniqueness on successful completion, even before UniqueFor
	// expires. Hold the first execution while testing duplicate rejection.
	if _, err := producer.Enqueue(t.Context(), task, options); !errors.Is(err, queue.ErrDuplicateTask) {
		t.Fatalf("signed UniqueFor was lost: %v", err)
	}
	releaseHandler()
	stored := waitIntegrityState(t, inspector, info.ID, backend.TaskStateCompleted)
	if string(stored.Payload) != "protected" || stored.Headers[integrityHeader] == "" {
		t.Fatal("signature missing or payload wrapped")
	}
	select {
	case delivered := <-processed:
		if len(delivered.Headers()) != 1 || delivered.Headers()["traceparent"] != "trace" {
			t.Fatal("integrity metadata leaked")
		}
	default:
		t.Fatal("handler not run")
	}
	if err := inspector.DeleteTask("default", info.ID); err != nil {
		t.Fatal(err)
	}
	raw := backend.NewClientFromRedisClient(client)
	if _, err := raw.Enqueue(backend.NewTaskWithHeaders(stored.Type, stored.Payload, stored.Headers), backend.TaskID(info.ID), backend.MaxRetry(0), backend.Timeout(time.Minute)); err != nil {
		t.Fatal(err)
	}
	replay := waitIntegrityState(t, inspector, info.ID, backend.TaskStateArchived)
	if !strings.Contains(replay.LastErr, ErrTaskReplay.Error()) {
		t.Fatalf("successful replay not stopped by trusted ledger: %s", replay.LastErr)
	}
	for _, mutation := range []string{"unsigned", "payload", "id", "queue", "max-retry"} {
		wire, id, err := signing.task(task, queue.EnqueueOptions{DisableRetry: true, Timeout: time.Minute}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		payload, headers := wire.Payload(), wire.Headers()
		var kind = wire.Type()
		queueName := "default"
		maxRetry := 0
		switch mutation {
		case "unsigned":
			delete(headers, integrityHeader)
		case "payload":
			payload = []byte("forged")
		case "id":
			id += "changed"
		case "queue":
			queueName = "other"
		case "max-retry":
			maxRetry = 1
		}
		if mutation == "queue" { // Direct verification establishes queue binding without starting another queue consumer.
			var envelope signedEnvelope
			_ = json.Unmarshal([]byte(headers[integrityHeader]), &envelope)
			envelope.Metadata.Queue = queueName
			encoded, _ := json.Marshal(envelope)
			headers[integrityHeader] = string(encoded)
			queueName = "default"
		}
		if _, err := raw.Enqueue(backend.NewTaskWithHeaders(kind, payload, headers), backend.TaskID(id), backend.Queue(queueName), backend.MaxRetry(maxRetry), backend.Timeout(time.Hour)); err != nil {
			t.Fatal(err)
		}
		archived := waitIntegrityState(t, inspector, id, backend.TaskStateArchived)
		if !strings.Contains(archived.LastErr, ErrTaskUnauthenticated.Error()) {
			t.Fatalf("mutation %s admitted: %s", mutation, archived.LastErr)
		}
	}
	select {
	case task := <-processed:
		t.Fatalf("forgery/replay reached handler: %s", task.Type())
	default:
	}
}

func waitIntegrityState(t *testing.T, inspector *backend.Inspector, id string, state backend.TaskState) *backend.TaskInfo {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		info, err := inspector.GetTaskInfo("default", id)
		if err != nil {
			t.Fatal(err)
		}
		if info.State == state {
			return info
		}
		select {
		case <-deadline.C:
			t.Fatalf("state=%s wanted=%s err=%s", info.State, state, info.LastErr)
		case <-ticker.C:
		}
	}
}

type delayedIntegrityStore struct{ *integrityTestStore }

func (store delayedIntegrityStore) Claim(ctx context.Context, identity TaskIdentity) (TaskLease, error) {
	lease, err := store.integrityTestStore.Claim(ctx, identity)
	if err != nil {
		return nil, err
	}
	<-ctx.Done() // Model a claim which committed before expiry but returned late.
	return lease, nil
}

func TestVerifiedWorkerLeaseFailuresAndLateClaims(t *testing.T) {
	for _, scenario := range []string{"claim-error", "nil-lease", "finish-error", "panic", "expiry-during-claim", "cancel-during-claim", "handler-timeout"} {
		t.Run(scenario, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			defer client.Close()
			store := &integrityTestStore{}
			var replay ReplayStore = store
			injected := errors.New("trusted ledger unavailable")
			switch scenario {
			case "claim-error":
				store.claimErr = injected
			case "nil-lease":
				store.nilLease = true
			case "finish-error":
				store.finishErr = injected
			case "expiry-during-claim", "cancel-during-claim":
				replay = delayedIntegrityStore{store}
			}
			signing, verification := integrityPolicies(t, replay)
			if scenario == "expiry-during-claim" {
				signing, _ = NewSigningPolicy(SigningConfig{KeyID: "key-1", Key: signing.config.Key, Audience: signing.config.Audience, ValidFor: time.Second})
			}
			worker, err := NewVerifiedWorkerFromRedisClient(client, WorkerConfig{Concurrency: 1, Queues: map[string]int{"default": 1}, ShutdownTimeout: time.Second, TaskCheckInterval: 50 * time.Millisecond}, verification)
			if err != nil {
				t.Fatal(err)
			}
			var calls int
			if err := worker.Start(queue.HandlerFunc(func(ctx context.Context, _ queue.Task) error {
				calls++
				if scenario == "panic" {
					panic("test handler failure")
				}
				if scenario == "handler-timeout" {
					<-ctx.Done()
					return nil
				}
				return nil
			})); err != nil {
				t.Fatal(err)
			}
			defer worker.Shutdown()
			task, _ := queue.NewTask("charge", nil, nil)
			wire, id, err := signing.task(task, queue.EnqueueOptions{DisableRetry: true, Timeout: 100 * time.Millisecond}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			timeout := time.Hour
			if scenario == "cancel-during-claim" {
				timeout = time.Second
			}
			raw := backend.NewClientFromRedisClient(client)
			if _, err := raw.Enqueue(wire, backend.TaskID(id), backend.MaxRetry(0), backend.Timeout(timeout)); err != nil {
				t.Fatal(err)
			}
			inspector := backend.NewInspectorFromRedisClient(client)
			archived := waitIntegrityState(t, inspector, id, backend.TaskStateArchived)
			// Inspector state completes after the handler's write, and Shutdown joins all processing goroutines.
			if err := worker.Shutdown(); err != nil {
				t.Fatal(err)
			}
			wantCalls := 0
			if scenario == "finish-error" || scenario == "panic" || scenario == "handler-timeout" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("%s reached handler %d times, wanted %d", scenario, calls, wantCalls)
			}
			if archived.LastErr == "" {
				t.Fatal("failure was treated as successful delivery")
			}
			store.mu.Lock()
			defer store.mu.Unlock()
			if record := store.records[id]; record != nil {
				if record.done {
					t.Fatal("failed execution recorded successful")
				}
				wantActive := scenario == "panic" || scenario == "finish-error"
				if record.active != wantActive {
					t.Fatalf("lease ownership active=%t wanted=%t", record.active, wantActive)
				}
			}
		})
	}
}

func TestVerifiedWorkerUsesTrustedAttemptBudgetAcrossWorkers(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	store := &integrityTestStore{}
	signing, verification := integrityPolicies(t, store)
	task, _ := queue.NewTask("charge", nil, nil)
	wire, id, err := signing.task(task, queue.EnqueueOptions{MaxRetry: 1, Timeout: time.Second}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw := backend.NewClientFromRedisClient(client)
	inspector := backend.NewInspectorFromRedisClient(client)
	calls := 0
	for attempt := 0; attempt < 3; attempt++ {
		// A new worker models a process replacement; the trusted store is shared.
		worker, err := NewVerifiedWorkerFromRedisClient(client, WorkerConfig{Concurrency: 1, Queues: map[string]int{"default": 1}, ShutdownTimeout: time.Second, TaskCheckInterval: 50 * time.Millisecond}, verification)
		if err != nil {
			t.Fatal(err)
		}
		if err := worker.Start(queue.HandlerFunc(func(context.Context, queue.Task) error { calls++; return errors.New("retryable business failure") })); err != nil {
			t.Fatal(err)
		}
		// Each raw enqueue resets Redis's retried count to zero. It cannot reset the independent budget.
		if _, err := raw.Enqueue(wire, backend.TaskID(id), backend.MaxRetry(1), backend.Timeout(time.Second)); err != nil {
			t.Fatal(err)
		}
		state := backend.TaskStateRetry
		if attempt == 2 {
			state = backend.TaskStateArchived
		}
		stored := waitIntegrityState(t, inspector, id, state)
		if err := worker.Shutdown(); err != nil {
			t.Fatal(err)
		}
		if attempt == 2 && !strings.Contains(stored.LastErr, ErrTaskReplay.Error()) {
			t.Fatalf("forged attempt counter bypassed budget: %s", stored.LastErr)
		}
		if err := inspector.DeleteTask("default", id); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("trusted retry budget was not enforced: %d", calls)
	}
	// Concurrent claims for one new identity must exclude all but one worker.
	identity := TaskIdentity{ID: "concurrent", Audience: "billing.production", ExpiresAt: time.Now().Add(time.Hour), MaxAttempts: 2}
	var group sync.WaitGroup
	results := make(chan error, 16)
	for range 16 {
		group.Go(func() { _, err := store.Claim(t.Context(), identity); results <- err })
	}
	group.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrTaskReplay) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("concurrent claims admitted %d workers", accepted)
	}
	// An old completion cannot release or undo a subsequent owner's claim.
	fenced := TaskIdentity{ID: "fenced", Audience: "billing.production", ExpiresAt: time.Now().Add(time.Hour), MaxAttempts: 2}
	first, err := store.Claim(t.Context(), fenced)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Finish(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	second, err := store.Claim(t.Context(), fenced)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Finish(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(t.Context(), fenced); !errors.Is(err, ErrTaskReplay) {
		t.Fatal("old lease released new ownership")
	}
	if err := second.Finish(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if err := first.Finish(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(t.Context(), fenced); !errors.Is(err, ErrTaskReplay) {
		t.Fatal("old lease undid successful completion")
	}
	// Model independently authorized recovery of an unfinished old owner.
	recovered := TaskIdentity{ID: "recovered", Audience: "billing.production", ExpiresAt: time.Now().Add(time.Hour), MaxAttempts: 2}
	stale, err := store.Claim(t.Context(), recovered)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.records[recovered.ID].active = false
	store.mu.Unlock()
	current, err := store.Claim(t.Context(), recovered)
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.Finish(t.Context(), false); !errors.Is(err, ErrTaskReplay) {
		t.Fatal("stale owner changed recovered claim")
	}
	store.mu.Lock()
	active := store.records[recovered.ID].active
	store.mu.Unlock()
	if !active {
		t.Fatal("stale owner released recovered claim")
	}
	if err := current.Finish(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if err := stale.Finish(t.Context(), false); !errors.Is(err, ErrTaskReplay) {
		t.Fatal("stale owner changed recovered terminal state")
	}
	store.mu.Lock()
	done := store.records[recovered.ID].done
	store.mu.Unlock()
	if !done {
		t.Fatal("stale owner undid recovered terminal state")
	}
}
