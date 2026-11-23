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

const namespaceV2Prefix = NamespacePrefix + "v2:"

// MaxLogicalKeyBytes 是业务键及单层命名空间前缀的字节上限。
const MaxLogicalKeyBytes = 1024

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

// IsTagMetadata recognizes direct tag keys and the exact frames emitted by
// NamespaceDriver, including nested wrappers. Markers embedded in a business key
// or inside a framed prefix do not determine the key's type.
func IsTagMetadata(key string) bool {
	// 已存元数据沿用原有的结构识别规则，避免配置上限影响既有保护。
	inner, valid := unframeNamespaces(key, len(key))
	if !valid {
		return false
	}
	namespaced := len(inner) != len(key)
	key = inner
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
	_, _, ok := unframe(metadata)
	return ok
}

// LogicalKey 剥离命名空间构造器允许的完整帧，并保留业务键本身的字节。
func LogicalKey(key string) (string, bool) {
	return unframeNamespaces(key, MaxLogicalKeyBytes)
}

func unframeNamespaces(key string, maxPrefixBytes int) (string, bool) {
	for strings.HasPrefix(key, namespaceV2Prefix) {
		prefix, tail, ok := unframe(key[len(namespaceV2Prefix):])
		if !ok || len(prefix) > maxPrefixBytes {
			return "", false
		}
		key = tail
	}
	return key, !strings.HasPrefix(key, NamespacePrefix)
}

// unframe 读取一个规范的十进制字节长度，返回帧内容和剩余键。
func unframe(value string) (string, string, bool) {
	separator := strings.IndexByte(value, ':')
	if separator <= 0 || separator > 1 && value[0] == '0' {
		return "", "", false
	}
	for index := 0; index < separator; index++ {
		if value[index] < '0' || value[index] > '9' {
			return "", "", false
		}
	}
	length, err := strconv.Atoi(value[:separator])
	if err != nil {
		return "", "", false
	}
	framed := value[separator+1:]
	if length >= len(framed) || framed[length] != ':' {
		return "", "", false
	}
	return framed[:length], framed[length+1:], true
}
