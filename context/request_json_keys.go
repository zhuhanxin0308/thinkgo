package context

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// inlineJSONKeyLimit 限制线性比较的规模；较大对象切换哈希表，避免字段数量导致平方级退化。
const inlineJSONKeyLimit = 8

// validationJSONKey 仅在同步校验期间借用未转义键的原始字节，不向调用方暴露或修改正文。
// 转义或无效 UTF-8 必须使用标准库解码，保证重名判断遵循相同的字符替换规则。
type validationJSONKey struct {
	raw     []byte
	decoded string
}

func (key validationJSONKey) text() string {
	if key.raw != nil {
		return string(key.raw)
	}
	return key.decoded
}

func (key validationJSONKey) equal(other validationJSONKey) bool {
	if key.raw != nil && other.raw != nil {
		return bytes.Equal(key.raw, other.raw)
	}
	if key.raw != nil {
		return string(key.raw) == other.decoded
	}
	if other.raw != nil {
		return key.decoded == string(other.raw)
	}
	return key.decoded == other.decoded
}

func (reader *strictJSONReader) readValidationKey() (validationJSONKey, error) {
	start := reader.offset
	escaped := reader.skipString()
	content := reader.body[start+1 : reader.offset-1]
	if !escaped && utf8.Valid(content) {
		return validationJSONKey{raw: content}, nil
	}
	var decoded string
	err := json.Unmarshal(reader.body[start:reader.offset], &decoded)
	return validationJSONKey{decoded: decoded}, err
}

// validateObject 对前几个键使用有界栈数组，常见小对象无需复制键字符串或分配字典。
// 每个对象超过固定上限后仅迁移一次；后续使用哈希查重，总体保持期望线性复杂度。
func (reader *strictJSONReader) validateObject(depth int) error {
	var inline [inlineJSONKeyLimit]validationJSONKey
	var expanded map[string]struct{}
	count := 0
	reader.offset++
	reader.skipWhitespace()
	if reader.body[reader.offset] == '}' {
		reader.offset++
		return nil
	}
	for {
		key, err := reader.readValidationKey()
		if err != nil {
			return err
		}
		if expanded == nil && count < len(inline) {
			for _, previous := range inline[:count] {
				if key.equal(previous) {
					return fmt.Errorf("JSON 对象包含重复键 %q", key.text())
				}
			}
			inline[count] = key
			count++
		} else {
			if expanded == nil {
				expanded = make(map[string]struct{}, len(inline)+1)
				for _, previous := range inline {
					expanded[previous.text()] = struct{}{}
				}
			}
			text := key.text()
			if _, duplicated := expanded[text]; duplicated {
				return fmt.Errorf("JSON 对象包含重复键 %q", text)
			}
			expanded[text] = struct{}{}
		}
		reader.skipWhitespace()
		reader.offset++ // 完整语法已验证，此处必然是冒号。
		if _, err := reader.readValue(depth + 1); err != nil {
			return err
		}
		reader.skipWhitespace()
		if reader.body[reader.offset] == '}' {
			reader.offset++
			return nil
		}
		reader.offset++ // 完整语法已验证，此处必然是成员分隔逗号。
		reader.skipWhitespace()
	}
}
