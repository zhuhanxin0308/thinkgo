package password

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Independently generated with argon2-cffi's Argon2id v19 implementation.
// Low costs exercise verification of legacy records, never the creation policy.
const referenceHash = "$argon2id$v=19$m=32,t=2,p=1$MDEyMzQ1Njc4OWFiY2RlZg$43RM7dHByC4AfpLczXye9i1AM9DP01Z+MAFHfDRnNJI"
const referencePassword = "correct horse battery staple"

func testHasher(t *testing.T) *Hasher {
	t.Helper()
	h, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestPasswordDefaultHashAndCheck(t *testing.T) {
	ctx := context.Background()
	first, err := HashPassword(ctx, referencePassword)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword(ctx, referencePassword)
	if err != nil {
		t.Fatal(err)
	}
	a, err := parse(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := parse(second)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || bytes.Equal(a.salt, b.salt) || a.config.MemoryKiB != 19456 || a.config.Iterations != 2 || a.config.Parallelism != 1 {
		t.Fatal("hashes must carry the default costs and independent random salts")
	}
	for _, sample := range []struct {
		plaintext string
		want      bool
	}{{referencePassword, true}, {"wrong password", false}} {
		matched, err := CheckPassword(ctx, first, sample.plaintext)
		if err != nil || matched != sample.want {
			t.Fatalf("verification: matched=%v err=%v", matched, err)
		}
	}
	if cap(defaultHasher.slots) != DefaultConfig().MaxConcurrent || len(defaultHasher.slots) != 0 {
		t.Fatal("shared concurrency budget drifted or leaked")
	}
}

func TestPasswordIndependentReferenceVectors(t *testing.T) {
	h := testHasher(t)
	for _, sample := range []struct{ encoded, plaintext string }{
		{referenceHash, referencePassword},
		{"$argon2id$v=19$m=32,t=2,p=1$MDEyMzQ1Njc4OWFiY2RlZg$Anjh+RiflQ75mGChMh04qpEJZNjWtR6XVxpkpcX2tR8", " 密码😀\x00 "},
	} {
		matched, err := h.Check(context.Background(), sample.encoded, sample.plaintext)
		if err != nil || !matched {
			t.Fatalf("independent vector rejected: match=%v err=%v", matched, err)
		}
		matched, err = h.Check(context.Background(), sample.encoded, sample.plaintext+"x")
		if err != nil || matched {
			t.Fatalf("altered password accepted: match=%v err=%v", matched, err)
		}
	}
}

func TestPasswordLengthAndByteIdentity(t *testing.T) {
	h := testHasher(t)
	ctx := context.Background()
	for _, input := range []string{"", strings.Repeat("a", MaxPasswordBytes+1), strings.Repeat("密", 342)} {
		if got, err := h.Hash(ctx, input); got != "" || !errors.Is(err, ErrPasswordLength) {
			t.Fatalf("invalid input hash: output length=%d err=%v", len(got), err)
		}
		if got, err := h.Check(ctx, referenceHash, input); got || !errors.Is(err, ErrPasswordLength) {
			t.Fatalf("invalid input verification: match=%v err=%v", got, err)
		}
	}
	// Use valid legacy costs to test byte semantics without weakening New.
	legacy := Config{MemoryKiB: 32, Iterations: 1, Parallelism: 1}
	for _, input := range []string{strings.Repeat("x", MaxPasswordBytes), " e\u0301\x00😀 ", "\xff\x00"} {
		if err := h.validate(ctx, input); err != nil {
			t.Fatal(err)
		}
		salt := []byte("0123456789abcdef")
		encoded := encode(legacy, salt, derive(input, salt, legacy))
		if ok, err := h.Check(ctx, encoded, input); err != nil || !ok {
			t.Fatal("full byte string did not verify", err)
		}
		if ok, err := h.Check(ctx, encoded, input[:len(input)-1]); err != nil || ok {
			t.Fatal("truncated/trimmed input verified", err)
		}
	}
}

func TestPasswordConfigurationAndZeroValue(t *testing.T) {
	config := DefaultConfig()
	h, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	config.MemoryKiB = 0
	if h.config.MemoryKiB != 19456 {
		t.Fatal("caller configuration aliases live configuration")
	}
	for _, change := range []func(*Config){
		func(c *Config) { *c = Config{} },
		func(c *Config) { c.MemoryKiB = 19455 },
		func(c *Config) { c.MemoryKiB = maxMemoryKiB + 4 },
		func(c *Config) { c.MemoryKiB++ },
		func(c *Config) { c.Iterations = 1 },
		func(c *Config) { c.Iterations = maxIterations + 1 },
		func(c *Config) { c.Parallelism = 0 },
		func(c *Config) { c.Parallelism = maxParallelism + 1 },
		func(c *Config) { c.MaxConcurrent = 0 },
		func(c *Config) { c.MaxConcurrent = -1 },
		func(c *Config) { c.MaxConcurrent = maxConcurrent + 1 },
	} {
		candidate := DefaultConfig()
		change(&candidate)
		if got, err := New(candidate); got != nil || !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("invalid configuration accepted: %+v err=%v", candidate, err)
		}
	}
	for _, invalid := range []*Hasher{nil, {}} {
		if _, err := invalid.Hash(context.Background(), "test"); !errors.Is(err, ErrInvalidConfig) {
			t.Fatal(err)
		}
		if _, err := invalid.Check(context.Background(), referenceHash, "test"); !errors.Is(err, ErrInvalidConfig) {
			t.Fatal(err)
		}
		if _, err := invalid.NeedsRehash(referenceHash); !errors.Is(err, ErrInvalidConfig) {
			t.Fatal(err)
		}
	}
}

// TestPasswordContextAdmission keeps deliberately invalid contexts as test
// inputs, not application call sites. No invalid context may emit a result or
// retain an operation slot; both reusable and shared APIs must reject it.
func TestPasswordContextAdmission(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer stop()
	for _, sample := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"nil", nil, ErrInvalidContext},
		{"canceled", canceled, context.Canceled},
		{"expired", expired, context.DeadlineExceeded},
	} {
		t.Run(sample.name, func(t *testing.T) {
			h := testHasher(t)
			for name, hash := range map[string]func(context.Context, string) (string, error){
				"hasher": h.Hash, "shared": HashPassword,
			} {
				t.Run(name+"/hash", func(t *testing.T) {
					if encoded, err := hash(sample.ctx, referencePassword); encoded != "" || !errors.Is(err, sample.want) {
						t.Fatalf("invalid context produced hash length=%d, err=%v; want %v", len(encoded), err, sample.want)
					}
				})
			}
			for name, check := range map[string]func(context.Context, string, string) (bool, error){
				"hasher": h.Check, "shared": CheckPassword,
			} {
				t.Run(name+"/check", func(t *testing.T) {
					if matched, err := check(sample.ctx, referenceHash, referencePassword); matched || !errors.Is(err, sample.want) {
						t.Fatalf("invalid context produced match=%v, err=%v; want %v", matched, err, sample.want)
					}
				})
			}
			if len(h.slots) != 0 || len(defaultHasher.slots) != 0 {
				t.Fatal("invalid context retained an operation slot")
			}
		})
	}
}

func TestPasswordEncodedHashBounds(t *testing.T) {
	h := testHasher(t)
	// No valid record can reach a KDF while the budget is occupied. Rejected
	// records must return their parse error, not ErrBusy: parsing precedes work.
	for range cap(h.slots) {
		h.slots <- struct{}{}
	}
	for name, invalid := range map[string]string{
		"empty": "", "oversized": strings.Repeat("x", maxEncodedBytes+1),
		"algorithm": strings.Replace(referenceHash, "argon2id", "argon2i", 1),
		"version":   strings.Replace(referenceHash, "v=19", "v=16", 1),
		"extra":     referenceHash + "$extra", "prefix": "x" + referenceHash,
		"missing cost":      strings.Replace(referenceHash, ",p=1", "", 1),
		"order":             strings.Replace(referenceHash, "m=32,t=2", "t=2,m=32", 1),
		"wrong time key":    strings.Replace(referenceHash, "t=2", "m=2", 1),
		"wrong lane key":    strings.Replace(referenceHash, "p=1", "t=1", 1),
		"memory sign":       strings.Replace(referenceHash, "m=32", "m=+32", 1),
		"memory padding":    strings.Replace(referenceHash, "m=32", "m=032", 1),
		"overflow":          strings.Replace(referenceHash, "m=32", "m=4294967296", 1),
		"time overflow":     strings.Replace(referenceHash, "t=2", "t=4294967296", 1),
		"lane overflow":     strings.Replace(referenceHash, "p=1", "p=4294967296", 1),
		"negative":          strings.Replace(referenceHash, "t=2", "t=-1", 1),
		"memory zero":       strings.Replace(referenceHash, "m=32", "m=0", 1),
		"memory budget":     strings.Replace(referenceHash, "m=32", "m=65540", 1),
		"memory rounding":   strings.Replace(referenceHash, "m=32", "m=33", 1),
		"time zero":         strings.Replace(referenceHash, "t=2", "t=0", 1),
		"time budget":       strings.Replace(referenceHash, "t=2", "t=11", 1),
		"lane zero":         strings.Replace(referenceHash, "p=1", "p=0", 1),
		"lane budget":       strings.Replace(referenceHash, "p=1", "p=5", 1),
		"salt padding":      strings.Replace(referenceHash, "Zg$", "Zg==$", 1),
		"salt invalid":      strings.Replace(referenceHash, "$MDEy", "$!DEy", 1),
		"salt noncanonical": strings.Replace(referenceHash, "Zg$", "Zh$", 1),
		"key padding":       referenceHash + "=",
		"key invalid":       referenceHash[:len(referenceHash)-1] + "!",
		"key noncanonical":  referenceHash[:len(referenceHash)-1] + "J",
		"newline":           strings.Replace(referenceHash, "$MDEy", "$MD\ny", 1),
	} {
		t.Run(name, func(t *testing.T) {
			matched, err := h.Check(context.Background(), invalid, "private-password")
			if matched || !(errors.Is(err, ErrInvalidHash) || errors.Is(err, ErrHashParameters)) {
				t.Fatalf("untrusted hash not rejected before KDF admission: match=%v err=%v", matched, err)
			}
			if strings.Contains(err.Error(), "private-password") || strings.Contains(err.Error(), "MDEy") {
				t.Fatal("error leaked password or record")
			}
		})
	}
	if matched, err := h.Check(context.Background(), referenceHash, referencePassword); matched || !errors.Is(err, ErrBusy) {
		t.Fatal("valid record must reach admission control", err)
	}
}

type failingEntropy struct{}

func (failingEntropy) Read([]byte) (int, error) {
	return 0, errors.New("internal random device details")
}

func TestPasswordEntropyFailureReleasesBudget(t *testing.T) {
	h := testHasher(t)
	for _, source := range []io.Reader{failingEntropy{}, bytes.NewReader(make([]byte, saltBytes-1))} {
		encoded, err := h.hash(context.Background(), "test-password", source)
		if encoded != "" || err != ErrRandomSource || len(h.slots) != 0 {
			t.Fatalf("failed randomness must not emit a hash or retain capacity: %v", err)
		}
	}
	got, err := h.hash(context.Background(), referencePassword, strings.NewReader("0123456789abcdef"))
	if err != nil || !strings.HasPrefix(got, "$argon2id$v=19$m=19456,t=2,p=1$") || len(h.slots) != 0 {
		t.Fatal("budget did not recover after failure", err)
	}
}

type blockingEntropy struct {
	entered chan struct{}
	release chan struct{}
	reads   atomic.Int32
}

func (r *blockingEntropy) Read(p []byte) (int, error) {
	r.reads.Add(1)
	close(r.entered)
	<-r.release
	return copy(p, "0123456789abcdef"), nil
}

func TestPasswordConcurrencyAndCancellation(t *testing.T) {
	config := DefaultConfig()
	config.MaxConcurrent = 1
	h, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	source := &blockingEntropy{entered: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	var unblock sync.Once
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() { defer close(finished); _, err := h.hash(ctx, referencePassword, source); done <- err }()
	t.Cleanup(func() { cancel(); unblock.Do(func() { close(source.release) }); <-finished })
	select {
	case <-source.entered:
	case err := <-done:
		t.Fatalf("hash did not reach salt generation: %v", err)
	} // The real Hash call owns the only slot and is reading salt.
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			if _, err := h.Hash(context.Background(), "other"); !errors.Is(err, ErrBusy) {
				t.Errorf("hash did not reject saturation: %v", err)
			}
			if _, err := h.Check(context.Background(), referenceHash, "other"); !errors.Is(err, ErrBusy) {
				t.Errorf("check did not share the budget: %v", err)
			}
		})
	}
	workers.Wait()
	cancel()
	// Cancellation must not make the in-flight operation's slot available early.
	if len(h.slots) != 1 {
		t.Fatal("cancellation released active work prematurely")
	}
	unblock.Do(func() { close(source.release) })
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(h.slots) != 0 || source.reads.Load() != 1 {
		t.Fatal("slot leaked or work started while saturated")
	}
	if _, err := h.Hash(ctx, "test"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := h.Check(ctx, referenceHash, "test"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := h.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if matched, err := h.Check(context.Background(), referenceHash, referencePassword); !matched || err != nil {
		t.Fatal("budget not reusable", err)
	}
}

func TestPasswordRehashPolicyDoesNotDowngrade(t *testing.T) {
	h := testHasher(t)
	for _, sample := range []struct {
		costs string
		want  bool
	}{
		{"m=32,t=1,p=1", true}, {"m=19456,t=1,p=1", true},
		{"m=19456,t=2,p=1", false}, {"m=32768,t=3,p=1", false},
		{"m=32768,t=1,p=1", false}, {"m=32,t=3,p=1", false},
		{"m=32,t=1,p=2", false},
	} {
		encoded := strings.Replace(referenceHash, "m=32,t=2,p=1", sample.costs, 1)
		got, err := h.NeedsRehash(encoded)
		if err != nil || got != sample.want {
			t.Fatalf("costs=%s got=%v err=%v", sample.costs, got, err)
		}
	}
	if got, err := h.NeedsRehash("invalid"); got || !errors.Is(err, ErrInvalidHash) {
		t.Fatal(err)
	}
}

func FuzzPasswordEncodedHash(f *testing.F) {
	for _, seed := range []string{referenceHash, "", "$argon2id$v=19$m=4294967295,t=9999,p=255$salt$key", strings.Repeat("x", 257)} {
		f.Add(seed)
	}
	h, err := New(DefaultConfig())
	if err != nil {
		f.Fatal(err)
	}
	for range cap(h.slots) {
		h.slots <- struct{}{}
	}
	f.Fuzz(func(t *testing.T, encoded string) {
		stored, parseErr := parse(encoded)
		matched, checkErr := h.Check(context.Background(), encoded, "fuzz-password")
		if matched {
			t.Fatal("verification admitted work without a free slot")
		}
		if parseErr != nil {
			if checkErr != parseErr {
				t.Fatal("validation/admission order changed")
			}
			return
		}
		if !errors.Is(checkErr, ErrBusy) {
			t.Fatal(checkErr)
		}
		if encode(stored.config, stored.salt, stored.key) != encoded {
			t.Fatal("parser accepted a noncanonical hash")
		}
		if stored.config.MemoryKiB > maxMemoryKiB || stored.config.Iterations > maxIterations || stored.config.Parallelism > maxParallelism {
			t.Fatal("parameter cap bypassed")
		}
	})
}

func BenchmarkPasswordHash(b *testing.B) {
	h, err := New(DefaultConfig())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := h.Hash(context.Background(), referencePassword); err != nil {
			b.Fatal(err)
		}
	}
}
