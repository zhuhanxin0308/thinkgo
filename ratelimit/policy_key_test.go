package ratelimit

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPolicyKeyIsStableAndSeparatesPolicies(t *testing.T) {
	limit := Limit{Rate: 1, Period: time.Minute, Burst: 1}
	key, err := PolicyKey("client", limit)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := PolicyKey(" client ", limit); err != nil || again != key {
		t.Fatalf("normalization/stability: %q %v", again, err)
	}
	for _, other := range []Limit{
		{Rate: 2, Period: time.Minute, Burst: 1},
		{Rate: 1, Period: time.Second, Burst: 1},
		{Rate: 1, Period: time.Minute, Burst: 2},
	} {
		if got, err := PolicyKey("client", other); err != nil || got == key {
			t.Fatalf("policies must have different keys: %+v %q %v", other, got, err)
		}
	}
	if other, err := PolicyKey("other:client", limit); err != nil || other == key {
		t.Fatalf("business namespace must be preserved: %q %v", other, err)
	}
}

func TestPolicyKeyValidatesBeforeHashing(t *testing.T) {
	limit := Limit{Rate: 1, Period: time.Minute, Burst: 1}
	for _, key := range []string{"", " ", "bad\x00key", "bad\r\nkey", strings.Repeat(" ", maximumKeyBytes) + "x"} {
		if _, err := PolicyKey(key, limit); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("invalid key accepted: length=%d error=%v", len(key), err)
		}
	}
	if key, err := PolicyKey(strings.Repeat("x", maximumKeyBytes), limit); err != nil || len(key) > maximumKeyBytes {
		t.Fatalf("maximum input must produce a bounded key: length=%d error=%v", len(key), err)
	}
	if _, err := PolicyKey("client", Limit{}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("invalid policy accepted: %v", err)
	}
}
