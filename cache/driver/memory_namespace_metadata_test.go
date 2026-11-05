package driver

import (
	"errors"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/cachekey"
)

func TestMemoryNamespaceMetadataSurvivesFIFOFromSetAndUpdate(t *testing.T) {
	writers := map[string]func(*Memory, string) error{
		"set": func(memory *Memory, key string) error {
			return memory.Set(key, int64(7), 0)
		},
		"update": func(memory *Memory, key string) error {
			return memory.Update(key, 0, func(interface{}, bool) (interface{}, bool, error) {
				return int64(7), false, nil
			})
		},
	}
	keys := map[string]string{
		"direct-forward": cacheTagMetadataPrefix + "default:users",
		"direct-reverse": cacheTagReverseMetadataPrefix + "default:digest",
		"empty-forward":  cachekey.Namespace("") + cacheTagMetadataPrefix + cachekey.TagNamespace("tag:") + "default:users",
		"custom-reverse": cachekey.Namespace("tenant:") + cacheTagReverseMetadataPrefix + cachekey.TagNamespace("custom:") + "default:digest",
		"unicode":        cachekey.Namespace("租户:") + cacheTagMetadataPrefix + cachekey.TagNamespace("标签:") + "default:users",
		"nested":         cachekey.Namespace("outer:") + cachekey.Namespace("inner:") + cacheTagMetadataPrefix + cachekey.TagNamespace("tag:") + "default:users",
	}
	for writerName, write := range writers {
		for keyName, key := range keys {
			t.Run(writerName+"/"+keyName, func(t *testing.T) {
				memory, err := NewMemoryWithMaxEntries(2)
				if err != nil {
					t.Fatal(err)
				}
				if err := write(memory, key); err != nil {
					t.Fatal(err)
				}
				fence := cacheFenceMetadataPrefix + "namespace:" + cachekey.Namespace("tenant:") + "sequence"
				if err := memory.Set(fence, int64(42), 0); err != nil {
					t.Fatal(err)
				}
				for _, business := range []string{"old", "new"} {
					if err := memory.Set(business, business, 0); err != nil {
						t.Fatal(err)
					}
				}
				requireMemoryValue(t, memory, key, int64(7))
				requireMemoryValue(t, memory, fence, int64(42))
				requireMemoryValue(t, memory, "new", "new")
				if _, found, err := memory.Get("old"); err != nil || found {
					t.Fatalf("oldest business entry was not evicted: found=%t err=%v", found, err)
				}
				if err := write(memory, key+":second"); !errors.Is(err, ErrMemoryCapacityExhausted) {
					t.Fatalf("new metadata should require available capacity: %v", err)
				}
				requireMemoryValue(t, memory, "new", "new")
				if err := memory.Delete("new"); err != nil {
					t.Fatal(err)
				}
				if err := write(memory, key+":second"); err != nil {
					t.Fatal(err)
				}
				if err := memory.Set("blocked", "value", 0); !errors.Is(err, ErrMemoryCapacityExhausted) {
					t.Fatalf("ordinary write evicted protected metadata: %v", err)
				}
				requireMemoryValue(t, memory, key, int64(7))
				requireMemoryValue(t, memory, key+":second", int64(7))
				requireMemoryValue(t, memory, fence, int64(42))
				assertMemoryCapacityAccounting(t, memory)
			})
		}
	}
}

func TestMemoryNamespaceLookalikesRemainEvictable(t *testing.T) {
	for name, key := range map[string]string{
		"marker-in-business":  "user:" + cacheTagMetadataPrefix + "members",
		"marker-in-prefix":    cachekey.Namespace(cacheTagMetadataPrefix) + "business",
		"namespaced-business": cachekey.Namespace("tenant:") + "user:" + cacheTagMetadataPrefix + "members",
		"bad-namespace-frame": "__thinkgo_namespace__:v2:07:tenant::__thinkgo_tag__:4:tag::members",
		"bad-tag-frame":       cachekey.Namespace("tenant:") + cacheTagMetadataPrefix + "04:tag::members",
		"nested-fence-marker": cachekey.Namespace("tenant:") + cacheFenceMetadataPrefix + "sequence",
	} {
		t.Run(name, func(t *testing.T) {
			memory, err := NewMemoryWithMaxEntries(1)
			if err != nil {
				t.Fatal(err)
			}
			if err := memory.Set(key, "ordinary", 0); err != nil {
				t.Fatal(err)
			}
			assertMemoryCapacityAccounting(t, memory)
			if err := memory.Set("replacement", "replacement", 0); err != nil {
				t.Fatalf("ordinary key was incorrectly protected: %v", err)
			}
			if _, found, err := memory.Get(key); err != nil || found {
				t.Fatalf("ordinary key escaped FIFO capacity: found=%t err=%v", found, err)
			}
			requireMemoryValue(t, memory, "replacement", "replacement")
		})
	}
}
