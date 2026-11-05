package cachekey

import (
	"strings"
	"testing"
)

func TestNamespaceFramingUsesByteLengths(t *testing.T) {
	for _, test := range []struct {
		prefix string
		frame  string
	}{
		{"", "0::"},
		{"tenant:", "7:tenant::"},
		{"a:b", "3:a:b:"},
		{"租户:", "7:租户::"},
	} {
		t.Run(test.prefix, func(t *testing.T) {
			if got, want := Namespace(test.prefix), "__thinkgo_namespace__:v2:"+test.frame; got != want {
				t.Fatalf("Namespace(%q)=%q, want %q", test.prefix, got, want)
			}
			if got := TagNamespace(test.prefix); got != test.frame {
				t.Fatalf("TagNamespace(%q)=%q, want %q", test.prefix, got, test.frame)
			}
		})
	}
}

func TestTagMetadataClassificationRespectsFramedBoundaries(t *testing.T) {
	for _, test := range []struct {
		name string
		key  string
		want bool
	}{
		{"direct-forward", "__thinkgo_tag__:default:users", true},
		{"direct-reverse", "__thinkgo_tag_reverse__:default:digest", true},
		{"direct-prefix-contract", "__thinkgo_tag__:", true},
		{"empty-namespace", "__thinkgo_namespace__:v2:0::__thinkgo_tag__:4:tag::default:users", true},
		{"custom-reverse", "__thinkgo_namespace__:v2:7:tenant::__thinkgo_tag_reverse__:7:custom::default:digest", true},
		{"unicode-byte-length", "__thinkgo_namespace__:v2:7:租户::__thinkgo_tag__:7:标签::default:users", true},
		{"nested-wrappers", Namespace("outer:") + Namespace("inner:") + TagMetadataPrefix + TagNamespace("tag:") + "default:users", true},
		{"markers-in-framed-prefixes", Namespace(TagReverseMetadataPrefix) + TagMetadataPrefix + TagNamespace(NamespacePrefix) + "default:users", true},
		{"empty", "", false},
		{"business", "user:1", false},
		{"embedded-marker", "user:__thinkgo_tag__:default:users", false},
		{"embedded-namespace", "user:" + Namespace("") + TagMetadataPrefix + TagNamespace("tag:") + "users", false},
		{"namespace-business", Namespace("tenant:") + "user:__thinkgo_tag__:default:users", false},
		{"marker-inside-namespace-prefix", Namespace(TagMetadataPrefix) + "user:1", false},
		{"namespace-without-key", Namespace("tenant:"), false},
		{"unframed-tag-prefix", Namespace("tenant:") + TagMetadataPrefix + "default:users", false},
		{"future-version", "__thinkgo_namespace__:v3:0::__thinkgo_tag__:4:tag::users", false},
		{"namespace-fence-is-not-tag", Namespace("tenant:") + "__thinkgo_cache_fence__:sequence", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := IsTagMetadata(test.key); got != test.want {
				t.Fatalf("IsTagMetadata(%q)=%t, want %t", test.key, got, test.want)
			}
		})
	}
}

func TestTagMetadataRejectsMalformedFrames(t *testing.T) {
	for _, test := range []struct {
		name  string
		frame string
	}{
		{"missing-length", ":x:"},
		{"missing-separator", "1"},
		{"missing-empty-boundary", "0:"},
		{"missing-value-boundary", "1:x"},
		{"wrong-boundary", "1:xy:"},
		{"truncated-prefix", "999:short:"},
		{"leading-zero", "01:x:"},
		{"multiple-zero", "00::"},
		{"negative-zero", "-0::"},
		{"negative", "-1:x:"},
		{"positive-sign", "+1:x:"},
		{"whitespace", " 1:x:"},
		{"non-decimal", "0x1:x:"},
		{"unicode-digit", "１:x:"},
		{"rune-length-instead-of-byte-length", "1:租:"},
		{"integer-overflow", strings.Repeat("9", 100) + ":x:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := namespaceV2Prefix + test.frame + TagMetadataPrefix + "4:tag::default:users"
			if IsTagMetadata(key) {
				t.Fatalf("malformed namespace frame was protected: %q", key)
			}
			key = Namespace("tenant:") + TagReverseMetadataPrefix + test.frame + "default:digest"
			if IsTagMetadata(key) {
				t.Fatalf("malformed tag-prefix frame was protected: %q", key)
			}
		})
	}
}
