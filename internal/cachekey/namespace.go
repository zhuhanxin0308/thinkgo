// Package cachekey defines the physical key framing shared by cache namespaces
// and backends that distinguish internal metadata from ordinary entries.
package cachekey

import (
	"strconv"
	"strings"
)

// NamespacePrefix reserves the physical namespace syntax from public keys.
const NamespacePrefix = "__thinkgo_namespace__:"

// TagMetadataPrefix marks a tag's forward member list.
const TagMetadataPrefix = "__thinkgo_tag__:"

// TagReverseMetadataPrefix marks the tags associated with a cache key.
const TagReverseMetadataPrefix = "__thinkgo_tag_reverse__:"

// LockMetadataPrefix marks an internal distributed or process cache lock key.
const LockMetadataPrefix = "__thinkgo_lock__:"

// FenceMetadataPrefix marks monotonic fencing sequence and watermark metadata.
const FenceMetadataPrefix = "__thinkgo_cache_fence__:"

const namespaceV2Prefix = NamespacePrefix + "v2:"

// Namespace returns the existing v2 byte-length frame for a store prefix.
func Namespace(prefix string) string {
	return namespaceV2Prefix + frame(prefix)
}

// TagNamespace returns the byte-length frame for a tag prefix.
func TagNamespace(prefix string) string {
	return frame(prefix)
}

func frame(value string) string {
	return strconv.Itoa(len(value)) + ":" + value + ":"
}

// UnframeNamespaces peels zero or more canonical v2 namespace frames and returns
// the inner logical key. Keys starting with NamespacePrefix that fail canonical
// v2 unframing report valid=false so callers can fail closed.
func UnframeNamespaces(key string) (inner string, namespaced bool, valid bool) {
	for strings.HasPrefix(key, namespaceV2Prefix) {
		var ok bool
		key, ok = unframe(key[len(namespaceV2Prefix):])
		if !ok {
			return "", true, false
		}
		namespaced = true
	}
	if strings.HasPrefix(key, NamespacePrefix) {
		return "", namespaced, false
	}
	return key, namespaced, true
}

// ClassifyProtectedMetadata distinguishes direct or canonically framed lock and
// fencing metadata keys from ordinary business keys. Markers embedded inside a
// business key or inside a valid namespace prefix frame are treated as ordinary
// business data; malformed namespace frames containing protected markers report
// ambiguous=true.
func ClassifyProtectedMetadata(key string) (protected bool, ambiguous bool) {
	inner, _, valid := UnframeNamespaces(key)
	if !valid {
		return false, strings.Contains(key, LockMetadataPrefix) || strings.Contains(key, FenceMetadataPrefix)
	}
	if strings.HasPrefix(inner, LockMetadataPrefix) || strings.HasPrefix(inner, FenceMetadataPrefix) {
		return true, false
	}
	return false, false
}

// IsTagMetadata recognizes direct tag keys and the exact frames emitted by
// NamespaceDriver, including nested wrappers. Markers embedded in a business key
// or inside a framed prefix do not determine the key's type.
func IsTagMetadata(key string) bool {
	key, namespaced, valid := UnframeNamespaces(key)
	if !valid {
		return false
	}
	metadata, tag := strings.CutPrefix(key, TagMetadataPrefix)
	if !tag {
		var reverse bool
		metadata, reverse = strings.CutPrefix(key, TagReverseMetadataPrefix)
		if !reverse {
			return false
		}
	}
	if !namespaced {
		// Direct keys predate namespace framing and retain their prefix contract.
		return true
	}
	_, ok := unframe(metadata)
	return ok
}

// unframe consumes one canonical decimal byte-length frame. It leaves the
// remaining key opaque and rejects truncated, overflowing or ambiguous frames.
func unframe(value string) (string, bool) {
	separator := strings.IndexByte(value, ':')
	if separator <= 0 || separator > 1 && value[0] == '0' {
		return "", false
	}
	for index := 0; index < separator; index++ {
		if value[index] < '0' || value[index] > '9' {
			return "", false
		}
	}
	length, err := strconv.Atoi(value[:separator])
	if err != nil {
		return "", false
	}
	framed := value[separator+1:]
	if length >= len(framed) || framed[length] != ':' {
		return "", false
	}
	return framed[length+1:], true
}
