package asynq

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	backend "github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/zhuhanxin0308/thinkgo/v3/queue"
)

const integrityHeader = "thinkgo-integrity-v1"

var (
	// ErrTaskUnauthenticated means the task failed authentication or its signed execution bounds.
	ErrTaskUnauthenticated = errors.New("queue task authentication failed")
	// ErrTaskReplay means a trusted replay store rejected a duplicate or exhausted execution.
	ErrTaskReplay = errors.New("queue task replay rejected")
)

// SigningConfig contains trusted producer configuration. Use a distinct Audience
// for each application/environment and a cryptographically random key of at least 32 bytes.
type SigningConfig struct {
	KeyID    string
	Key      []byte
	Audience string
	ValidFor time.Duration
}

// SigningPolicy is an immutable signing configuration. Its zero value is invalid.
type SigningPolicy struct{ config SigningConfig }

// NewSigningPolicy validates and copies the key. ValidFor covers scheduling,
// queue delay, every retry and the final execution; expiry is never extended by Redis.
func NewSigningPolicy(config SigningConfig) (*SigningPolicy, error) {
	if !validIntegrityName(config.KeyID) || !validIntegrityName(config.Audience) ||
		len(config.Key) < 32 || len(config.Key) > 4096 || config.ValidFor < time.Second || config.ValidFor > queue.MaximumRetention {
		return nil, fmt.Errorf("%w: invalid signing policy", queue.ErrInvalidOptions)
	}
	config.Key = bytes.Clone(config.Key)
	return &SigningPolicy{config: config}, nil
}

// TaskIdentity describes an authenticated logical task, independent of Redis's
// mutable retry counter. ExpiresAt includes the verifier's clock-skew allowance.
type TaskIdentity struct {
	ID          string
	Audience    string
	ExpiresAt   time.Time
	MaxAttempts int
}

// ReplayStore must use storage independently trusted from the task Redis.
// Claim atomically excludes concurrent executions, counts attempts itself and
// rejects completed/exhausted tasks with ErrTaskReplay. Persist the identity and
// terminal record until ExpiresAt. Never authorize an attempt from Redis's counter.
// On a crash or an uncertain Finish, recovery must preserve business idempotency;
// blindly expiring a lease can duplicate effects. All workers share this store.
type ReplayStore interface {
	Claim(context.Context, TaskIdentity) (TaskLease, error)
}

// TaskLease belongs to one successful Claim. Finish records success as terminal;
// failure permits another claim only within the original MaxAttempts and expiry.
// Each lease needs a unique ownership token: Finish is idempotent, atomically
// checks that token and cannot change a later claim. Finish must fail closed on
// an uncertain outcome. Handler effects
// and this record require a business transaction or idempotency strategy.
type TaskLease interface {
	Finish(context.Context, bool) error
}

// VerificationConfig contains trusted worker configuration. Keys can include
// the old and current key IDs during rotation. Replay is mandatory.
type VerificationConfig struct {
	Keys        map[string][]byte
	Audience    string
	MaxValidFor time.Duration
	ClockSkew   time.Duration
	Replay      ReplayStore
}

// VerificationPolicy is immutable except for its caller-owned concurrent ReplayStore.
// Its zero value is invalid.
type VerificationPolicy struct{ config VerificationConfig }

// NewVerificationPolicy snapshots the key ring and validates execution bounds.
func NewVerificationPolicy(config VerificationConfig) (*VerificationPolicy, error) {
	if !validIntegrityName(config.Audience) || config.MaxValidFor < time.Second || config.MaxValidFor > queue.MaximumRetention ||
		config.ClockSkew < 0 || config.ClockSkew > 5*time.Minute || len(config.Keys) == 0 || len(config.Keys) > 64 || nilIntegrityValue(config.Replay) {
		return nil, fmt.Errorf("%w: invalid verification policy", queue.ErrInvalidOptions)
	}
	keys := make(map[string][]byte, len(config.Keys))
	for id, key := range config.Keys {
		if !validIntegrityName(id) || len(key) < 32 || len(key) > 4096 {
			return nil, fmt.Errorf("%w: invalid verification key", queue.ErrInvalidOptions)
		}
		keys[id] = bytes.Clone(key)
	}
	config.Keys = keys
	return &VerificationPolicy{config: config}, nil
}

// NewSignedProducer creates a producer that authenticates each task and assigns a fresh signed ID.
func NewSignedProducer(config RedisConfig, policy *SigningPolicy) (*Producer, error) {
	if policy == nil || len(policy.config.Key) == 0 {
		return nil, queue.ErrInvalidOptions
	}
	producer, err := NewProducer(config)
	if err == nil {
		producer.signing = policy
	}
	return producer, err
}

// NewSignedProducerFromRedisClient creates a signed producer using a borrowed client.
func NewSignedProducerFromRedisClient(client redis.UniversalClient, policy *SigningPolicy) (*Producer, error) {
	if policy == nil || len(policy.config.Key) == 0 {
		return nil, queue.ErrInvalidOptions
	}
	producer, err := NewProducerFromRedisClient(client)
	if err == nil {
		producer.signing = policy
	}
	return producer, err
}

// NewVerifiedWorker creates a worker that requires authentication and replay admission before dispatch.
func NewVerifiedWorker(config RedisConfig, workerConfig WorkerConfig, policy *VerificationPolicy) (*Worker, error) {
	if policy == nil || len(policy.config.Keys) == 0 {
		return nil, queue.ErrInvalidOptions
	}
	worker, err := NewWorker(config, workerConfig)
	if err == nil {
		worker.verification = policy
	}
	return worker, err
}

// NewVerifiedWorkerFromRedisClient creates a verified worker using a borrowed client.
func NewVerifiedWorkerFromRedisClient(client redis.UniversalClient, config WorkerConfig, policy *VerificationPolicy) (*Worker, error) {
	if policy == nil || len(policy.config.Keys) == 0 {
		return nil, queue.ErrInvalidOptions
	}
	worker, err := NewWorkerFromRedisClient(client, config)
	if err == nil {
		worker.verification = policy
	}
	return worker, err
}

type signedMetadata struct {
	Version   int    `json:"version"`
	KeyID     string `json:"key_id"`
	Audience  string `json:"audience"`
	ID        string `json:"id"`
	Type      string `json:"type"`
	Queue     string `json:"queue"`
	Issued    int64  `json:"issued"`
	NotBefore int64  `json:"not_before"`
	Expires   int64  `json:"expires"`
	MaxRetry  int    `json:"max_retry"`
	Timeout   int64  `json:"timeout_ns"`
}

type signedEnvelope struct {
	Metadata signedMetadata `json:"metadata"`
	MAC      string         `json:"mac"`
}

// JSON's sorted string-map keys and fixed struct field order define version 1's
// canonical encoding. Payload remains unchanged so Asynq UniqueFor retains its semantics.
func integrityMAC(metadata signedMetadata, payload []byte, headers map[string]string, key []byte) []byte {
	digest := sha256.Sum256(payload)
	encoded, _ := json.Marshal(struct {
		Domain   string            `json:"domain"`
		Metadata signedMetadata    `json:"metadata"`
		Payload  string            `json:"payload_sha256"`
		Headers  map[string]string `json:"headers"`
	}{"thinkgo/asynq/task/v1", metadata, hex.EncodeToString(digest[:]), headers})
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(encoded)
	return mac.Sum(nil)
}

func (policy *SigningPolicy) task(task queue.Task, options queue.EnqueueOptions, now time.Time) (*backend.Task, string, error) {
	if task.Type() == "" {
		return nil, "", queue.ErrInvalidTask
	}
	if err := options.Validate(now); err != nil {
		return nil, "", err
	}
	options = signedScheduleOptions(options)
	if !options.ProcessAt.IsZero() && !options.ProcessAt.Add(options.EffectiveTimeout()).Before(now.Add(policy.config.ValidFor)) ||
		options.ProcessAt.IsZero() && options.EffectiveTimeout() >= policy.config.ValidFor {
		return nil, "", fmt.Errorf("%w: signed validity must cover schedule and execution timeout", queue.ErrInvalidOptions)
	}
	headers := task.Headers()
	for name := range headers {
		if strings.EqualFold(name, integrityHeader) {
			return nil, "", fmt.Errorf("%w: reserved integrity header", queue.ErrInvalidTask)
		}
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, "", fmt.Errorf("%w: generate task identity: %w", queue.ErrBackendUnavailable, err)
	}
	id := hex.EncodeToString(random[:])
	metadata := signedMetadata{
		Version: 1, KeyID: policy.config.KeyID, Audience: policy.config.Audience,
		ID: id, Type: task.Type(), Queue: options.NormalizedQueue(), Issued: now.UnixNano(),
		NotBefore: now.UnixNano(), Expires: now.Add(policy.config.ValidFor).UnixNano(),
		MaxRetry: options.EffectiveMaxRetry(), Timeout: int64(options.EffectiveTimeout()),
	}
	if !options.ProcessAt.IsZero() {
		metadata.NotBefore = options.ProcessAt.UnixNano()
	}
	payload := task.Payload()
	encoded, err := json.Marshal(signedEnvelope{metadata, hex.EncodeToString(integrityMAC(metadata, payload, headers, policy.config.Key))})
	if err != nil {
		return nil, "", err
	}
	headers[integrityHeader] = string(encoded)
	return backend.NewTaskWithHeaders(task.Type(), payload, headers), id, nil
}

// Asynq persists schedule/timeout values at whole-second precision. Round a
// signed schedule upward so a legitimate task cannot be pulled before NotBefore.
func signedScheduleOptions(options queue.EnqueueOptions) queue.EnqueueOptions {
	if !options.ProcessAt.IsZero() && options.ProcessAt.Nanosecond() != 0 {
		options.ProcessAt = options.ProcessAt.Truncate(time.Second).Add(time.Second)
	}
	return options
}

func signedBackendTimeout(timeout time.Duration) time.Duration {
	return ((timeout + time.Second - 1) / time.Second) * time.Second
}

func (policy *VerificationPolicy) verify(task *backend.Task, now time.Time) (queue.Task, signedMetadata, error) {
	invalid := func() (queue.Task, signedMetadata, error) {
		return queue.Task{}, signedMetadata{}, ErrTaskUnauthenticated
	}
	if task == nil || len(task.Payload()) > queue.MaximumPayloadBytes || len(task.Headers()) > queue.MaximumHeaders+1 {
		return invalid()
	}
	headers := make(map[string]string, len(task.Headers()))
	for name, value := range task.Headers() {
		if name != integrityHeader {
			headers[name] = value
		}
	}
	raw := task.Headers()[integrityHeader]
	if len(raw) == 0 || len(raw) > 2048 {
		return invalid()
	}
	var envelope signedEnvelope
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || decoder.Decode(new(interface{})) != io.EOF {
		return invalid()
	}
	metadata := envelope.Metadata
	// Bound untrusted header syntax and bytes before canonical JSON/HMAC encoding.
	converted, err := queue.NewTask(task.Type(), task.Payload(), headers)
	if err != nil {
		return invalid()
	}
	key := policy.config.Keys[metadata.KeyID]
	mac, err := hex.DecodeString(envelope.MAC)
	if err != nil || len(key) == 0 || metadata.Version != 1 || metadata.Audience != policy.config.Audience ||
		metadata.Type != task.Type() || !hmac.Equal(mac, integrityMAC(metadata, task.Payload(), headers, key)) {
		return invalid()
	}
	issued, notBefore, expires := time.Unix(0, metadata.Issued), time.Unix(0, metadata.NotBefore), time.Unix(0, metadata.Expires)
	if metadata.Issued <= 0 || !expires.After(issued) || expires.After(issued.Add(policy.config.MaxValidFor)) ||
		issued.After(now.Add(policy.config.ClockSkew)) || notBefore.Before(issued) || notBefore.After(now.Add(policy.config.ClockSkew)) ||
		!notBefore.Before(expires) || !now.Before(expires.Add(policy.config.ClockSkew)) ||
		metadata.MaxRetry < 0 || metadata.MaxRetry > queue.MaximumRetries || metadata.Timeout <= 0 || metadata.Timeout > int64(queue.MaximumTaskTimeout) ||
		len(metadata.ID) != 64 {
		return invalid()
	}
	if _, err := hex.DecodeString(metadata.ID); err != nil {
		return invalid()
	}
	return converted, metadata, nil
}

func (policy *VerificationPolicy) handle(ctx context.Context, task *backend.Task, handler queue.Handler) error {
	converted, metadata, err := policy.verify(task, time.Now())
	if err != nil {
		return errors.Join(err, backend.SkipRetry)
	}
	id, hasID := backend.GetTaskID(ctx)
	name, hasQueue := backend.GetQueueName(ctx)
	maxRetry, hasMaxRetry := backend.GetMaxRetry(ctx)
	retries, hasRetries := backend.GetRetryCount(ctx)
	if !hasID || !hasQueue || !hasMaxRetry || !hasRetries || id != metadata.ID || name != metadata.Queue ||
		maxRetry != metadata.MaxRetry || retries < 0 || retries > metadata.MaxRetry {
		return errors.Join(ErrTaskUnauthenticated, backend.SkipRetry)
	}
	identity := TaskIdentity{ID: id, Audience: metadata.Audience,
		ExpiresAt: time.Unix(0, metadata.Expires).Add(policy.config.ClockSkew), MaxAttempts: metadata.MaxRetry + 1}
	claimContext, cancelClaim := context.WithDeadline(ctx, identity.ExpiresAt)
	defer cancelClaim()
	if err := claimContext.Err(); err != nil {
		return err
	}
	lease, err := policy.config.Replay.Claim(claimContext, identity)
	if err != nil {
		if errors.Is(err, ErrTaskReplay) {
			return errors.Join(err, backend.SkipRetry)
		}
		return err
	}
	if nilIntegrityValue(lease) {
		return fmt.Errorf("%w: replay store returned no lease", queue.ErrBackendUnavailable)
	}
	// Enforce the authenticated limit even if Redis's timeout was altered.
	execution, cancel := context.WithTimeout(claimContext, time.Duration(metadata.Timeout))
	defer cancel()
	result := execution.Err()
	if result == nil {
		result = handler.HandleTask(execution, converted)
	}
	if result == nil && execution.Err() != nil {
		result = execution.Err()
	}
	// Cancellation must not prevent recording the owned lease's outcome.
	completion, complete := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer complete()
	return errors.Join(result, lease.Finish(completion, result == nil))
}

func validIntegrityName(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("._:-", char)) {
			return false
		}
	}
	return true
}

func nilIntegrityValue(value interface{}) bool {
	if value == nil {
		return true
	}
	current := reflect.ValueOf(value)
	switch current.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return current.IsNil()
	}
	return false
}
