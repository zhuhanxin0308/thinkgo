// Package password provides bounded Argon2id password hashing and verification.
// Reuse a Hasher across requests; constructing one per request defeats its
// concurrency limit. Password policy, rate limiting and authentication remain
// application responsibilities.
package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	// MaxPasswordBytes bounds UTF-8 or arbitrary byte strings without truncation.
	MaxPasswordBytes = 1024
	maxEncodedBytes  = 256
	saltBytes        = 16
	keyBytes         = 32
	maxMemoryKiB     = 64 * 1024
	maxIterations    = 10
	maxParallelism   = 4
	maxConcurrent    = 16
)

var (
	ErrInvalidConfig  = errors.New("password: invalid hasher configuration")
	ErrInvalidContext = errors.New("password: context must not be nil")
	ErrPasswordLength = errors.New("password: password must contain 1 to 1024 bytes")
	ErrInvalidHash    = errors.New("password: invalid or unsupported encoded hash")
	ErrHashParameters = errors.New("password: encoded hash exceeds supported parameter bounds")
	ErrBusy           = errors.New("password: hashing concurrency limit reached")
	ErrRandomSource   = errors.New("password: secure salt generation failed")
)

// Config is copied by New and cannot be changed on an existing Hasher.
// MemoryKiB is total Argon2 memory, not a per-lane allocation. New hashes require
// at least 19 MiB and two passes. Verification permits older, weaker parameters
// but never more than 64 MiB, ten passes and four lanes per operation.
// MaxConcurrent bounds active operations on this Hasher, not the whole process.
type Config struct {
	MemoryKiB     uint32
	Iterations    uint32
	Parallelism   uint8
	MaxConcurrent int
}

// DefaultConfig uses 19 MiB, two passes, one lane and two active operations.
// Benchmark under deployment load before selecting higher costs/concurrency.
func DefaultConfig() Config {
	return Config{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1, MaxConcurrent: 2}
}

// Hasher is safe for concurrent use after construction with New. Do not copy it.
// Its zero value and a nil receiver reject operations with ErrInvalidConfig.
// Saturated operations return ErrBusy without queueing or starting a goroutine.
type Hasher struct {
	config Config
	slots  chan struct{}
}

// New validates a complete configuration. Use DefaultConfig for defaults;
// zero fields are not silently replaced. Encoded hashes never set these limits.
func New(config Config) (*Hasher, error) {
	if config.MemoryKiB < 19*1024 || config.MemoryKiB > maxMemoryKiB ||
		config.Iterations < 2 || config.Iterations > maxIterations ||
		config.Parallelism == 0 || config.Parallelism > maxParallelism ||
		config.MemoryKiB%(4*uint32(config.Parallelism)) != 0 ||
		config.MaxConcurrent < 1 || config.MaxConcurrent > maxConcurrent {
		return nil, ErrInvalidConfig
	}
	return &Hasher{config: config, slots: make(chan struct{}, config.MaxConcurrent)}, nil
}

// The convenience functions share a single process-local concurrency budget.
var defaultHasher = &Hasher{config: DefaultConfig(), slots: make(chan struct{}, 2)}

// HashPassword hashes plaintext using the shared default Hasher.
func HashPassword(ctx context.Context, plaintext string) (string, error) {
	return defaultHasher.Hash(ctx, plaintext)
}

// CheckPassword checks an encoded hash using the shared default Hasher.
// A password mismatch is (false, nil); malformed/expensive hashes return errors.
func CheckPassword(ctx context.Context, encoded, plaintext string) (bool, error) {
	return defaultHasher.Check(ctx, encoded, plaintext)
}

// Hash creates an Argon2id v19 PHC string containing parameters, salt and key.
// It does not trim, normalize, truncate or retain plaintext. Empty passwords are
// rejected; embedded NUL bytes are not removed. Cancellation is checked before
// and after hashing: the underlying KDF cannot be interrupted once started.
func (h *Hasher) Hash(ctx context.Context, plaintext string) (string, error) {
	return h.hash(ctx, plaintext, rand.Reader)
}

func (h *Hasher) hash(ctx context.Context, plaintext string, random io.Reader) (string, error) {
	if err := h.validate(ctx, plaintext); err != nil {
		return "", err
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	var salt [saltBytes]byte
	if _, err := io.ReadFull(random, salt[:]); err != nil {
		return "", ErrRandomSource
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key := derive(plaintext, salt[:], h.config)
	defer clear(key)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return encode(h.config, salt[:], key), nil
}

// Check verifies only the bounded Argon2id format produced by this package.
// It also accepts weaker parameters within the documented verification bounds
// for explicit upgrades. Malformed input is rejected before KDF allocation.
// Cancellation never releases a slot while the KDF is still running.
func (h *Hasher) Check(ctx context.Context, encoded, plaintext string) (bool, error) {
	if err := h.validate(ctx, plaintext); err != nil {
		return false, err
	}
	stored, err := parse(encoded)
	if err != nil {
		return false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	actual := derive(plaintext, stored.salt, stored.config)
	defer clear(actual)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(actual, stored.key) == 1, nil
}

// NeedsRehash reports a strictly monotonic memory/pass upgrade at the same lane
// count. It returns false for stronger or incomparable policies so callers do
// not accidentally downgrade a stored hash. A false result does not validate a
// password. Rehash only after Check succeeds, then compare-and-swap the stored
// hash to avoid overwriting a concurrent password change. Other algorithm/lane
// migrations require an explicit application policy.
func (h *Hasher) NeedsRehash(encoded string) (bool, error) {
	if h == nil || h.slots == nil {
		return false, ErrInvalidConfig
	}
	stored, err := parse(encoded)
	if err != nil {
		return false, err
	}
	old, target := stored.config, h.config
	return old.Parallelism == target.Parallelism &&
		old.MemoryKiB <= target.MemoryKiB && old.Iterations <= target.Iterations &&
		(old.MemoryKiB < target.MemoryKiB || old.Iterations < target.Iterations), nil
}

func (h *Hasher) validate(ctx context.Context, plaintext string) error {
	if h == nil || h.slots == nil {
		return ErrInvalidConfig
	}
	if ctx == nil {
		return ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(plaintext) == 0 || len(plaintext) > MaxPasswordBytes {
		return ErrPasswordLength
	}
	return nil
}

func (h *Hasher) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case h.slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			h.release()
			return err
		}
		return nil
	default:
		return ErrBusy
	}
}

func (h *Hasher) release() { <-h.slots }

func derive(plaintext string, salt []byte, config Config) []byte {
	input := []byte(plaintext)
	defer clear(input)
	return argon2.IDKey(input, salt, config.Iterations, config.MemoryKiB, config.Parallelism, keyBytes)
}

func encode(config Config, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version,
		config.MemoryKiB, config.Iterations, config.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// parsedHash contains only public encoded metadata, never the password.
type parsedHash struct {
	config Config
	salt   []byte
	key    []byte
}

func parse(encoded string) (parsedHash, error) {
	if len(encoded) > maxEncodedBytes {
		return parsedHash{}, ErrInvalidHash
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return parsedHash{}, ErrInvalidHash
	}
	costs := strings.Split(parts[3], ",")
	if len(costs) != 3 {
		return parsedHash{}, ErrInvalidHash
	}
	memory, err := parameter(costs[0], "m=")
	if err != nil {
		return parsedHash{}, err
	}
	iterations, err := parameter(costs[1], "t=")
	if err != nil {
		return parsedHash{}, err
	}
	parallelism, err := parameter(costs[2], "p=")
	if err != nil {
		return parsedHash{}, err
	}
	if parallelism == 0 || parallelism > maxParallelism || iterations == 0 ||
		iterations > maxIterations || memory < 8*parallelism || memory > maxMemoryKiB ||
		memory%(4*parallelism) != 0 {
		return parsedHash{}, ErrHashParameters
	}
	salt, err := component(parts[4], saltBytes)
	if err != nil {
		return parsedHash{}, err
	}
	key, err := component(parts[5], keyBytes)
	if err != nil {
		return parsedHash{}, err
	}
	return parsedHash{
		config: Config{MemoryKiB: memory, Iterations: iterations, Parallelism: uint8(parallelism)},
		salt:   salt, key: key,
	}, nil
}

// Require canonical decimal integers and canonical unpadded base64. In
// particular, DecodeString's acceptance of CR/LF must not relax the format.
func parameter(field, prefix string) (uint32, error) {
	if !strings.HasPrefix(field, prefix) {
		return 0, ErrInvalidHash
	}
	text := strings.TrimPrefix(field, prefix)
	value, err := strconv.ParseUint(text, 10, 32)
	if err != nil || strconv.FormatUint(value, 10) != text {
		return 0, ErrInvalidHash
	}
	return uint32(value), nil
}

func component(text string, length int) ([]byte, error) {
	if len(text) != base64.RawStdEncoding.EncodedLen(length) {
		return nil, ErrInvalidHash
	}
	decoded, err := base64.RawStdEncoding.Strict().DecodeString(text)
	if err != nil || len(decoded) != length || base64.RawStdEncoding.EncodeToString(decoded) != text {
		return nil, ErrInvalidHash
	}
	return decoded, nil
}
