package context

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"unicode/utf8"
)

const maxJSONNestingDepth = 100

// decodeStrictJSONObject 解析参数对象，并拒绝重复键、尾随文档和过深嵌套。
func decodeStrictJSONObject(body []byte) (map[string]interface{}, error) {
	value, err := decodeStrictJSONValue(body)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]interface{})
	if !ok {
		return nil, errors.New("JSON 根节点必须是对象")
	}
	return object, nil
}

// decodeStrictJSONTarget 在绑定前先完整校验文档，避免标准解码器覆盖重复键。
func decodeStrictJSONTarget(body []byte, target interface{}) error {
	if target == nil {
		return errors.New("JSON 绑定目标不能为空")
	}
	targetValue := reflect.ValueOf(target)
	if targetValue.Kind() != reflect.Pointer || targetValue.IsNil() {
		return errors.New("JSON 绑定目标必须是非空指针")
	}
	if err := validateStrictJSONDocument(body, false); err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	return nil
}

func decodeJSONTarget(body []byte, target interface{}) error {
	if target == nil {
		return errors.New("JSON 绑定目标不能为空")
	}
	targetValue := reflect.ValueOf(target)
	if targetValue.Kind() != reflect.Pointer || targetValue.IsNil() {
		return errors.New("JSON 绑定目标必须是非空指针")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func decodeStrictJSONValue(body []byte) (interface{}, error) {
	if err := validateJSONSyntax(body); err != nil {
		return nil, err
	}
	reader := strictJSONReader{body: body, buildValues: true}
	return reader.readValue(0)
}

// validateStrictJSONDocument 完整验证文档边界和对象键，不分配业务暂未使用的值树。
func validateStrictJSONDocument(body []byte, objectRoot bool) error {
	if err := validateJSONSyntax(body); err != nil {
		return err
	}
	reader := strictJSONReader{body: body}
	if _, err := reader.readValue(0); err != nil {
		return err
	}
	if objectRoot && bytes.TrimSpace(body)[0] != '{' {
		return errors.New("JSON 根节点必须是对象")
	}
	return nil
}

func validateJSONSyntax(body []byte) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return errors.New("JSON 请求体不能为空")
	}
	// 标准库一次验证完整文档，后续只读取已经确认合法的结构，避免逐 Token
	// 重复启动标量解码器。错误路径仍由标准库给出语法错误及字节位置。
	if !json.Valid(body) {
		var invalid json.RawMessage
		err := json.Unmarshal(body, &invalid)
		return err
	}
	return nil
}

// strictJSONReader 只接收 json.Valid 校验成功的完整文档，可仅校验结构或构造参数树。
// 每个字节最多经结构读取和字符串解码各扫描一次，嵌套深度沿用请求边界。
type strictJSONReader struct {
	body          []byte
	offset        int
	buildValues   bool
	keysValidated bool
}

func (reader *strictJSONReader) readValue(depth int) (interface{}, error) {
	if depth > maxJSONNestingDepth {
		return nil, fmt.Errorf("JSON 嵌套深度不能超过 %d", maxJSONNestingDepth)
	}
	reader.skipWhitespace()
	switch reader.body[reader.offset] {
	case '{':
		return reader.readObject(depth)
	case '[':
		return reader.readArray(depth)
	case '"':
		if !reader.buildValues {
			reader.skipString()
			return nil, nil
		}
		return reader.readString()
	case 't':
		reader.offset += len("true")
		return true, nil
	case 'f':
		reader.offset += len("false")
		return false, nil
	case 'n':
		reader.offset += len("null")
		return nil, nil
	default:
		start := reader.offset
		for reader.offset < len(reader.body) {
			switch reader.body[reader.offset] {
			case ',', ']', '}', ' ', '\t', '\r', '\n':
				if !reader.buildValues {
					return nil, nil
				}
				return json.Number(string(reader.body[start:reader.offset])), nil
			}
			reader.offset++
		}
		// 保留原始数字字面量，与 Decoder.UseNumber 的精度和指数表示一致。
		if !reader.buildValues {
			return nil, nil
		}
		return json.Number(string(reader.body[start:reader.offset])), nil
	}
}

func (reader *strictJSONReader) readObject(depth int) (interface{}, error) {
	if !reader.buildValues {
		return nil, reader.validateObject(depth)
	}
	object := make(map[string]interface{})
	reader.offset++
	reader.skipWhitespace()
	if reader.body[reader.offset] == '}' {
		reader.offset++
		return object, nil
	}
	for {
		key, err := reader.readString()
		if err != nil {
			return nil, err
		}
		if !reader.keysValidated {
			if _, duplicated := object[key]; duplicated {
				return nil, fmt.Errorf("JSON 对象包含重复键 %q", key)
			}
		}
		reader.skipWhitespace()
		reader.offset++ // 完整语法已验证，此处必然是冒号。
		value, err := reader.readValue(depth + 1)
		if err != nil {
			return nil, err
		}
		object[key] = value
		reader.skipWhitespace()
		if reader.body[reader.offset] == '}' {
			reader.offset++
			return object, nil
		}
		reader.offset++ // 完整语法已验证，此处必然是成员分隔逗号。
		reader.skipWhitespace()
	}
}

func (reader *strictJSONReader) readArray(depth int) (interface{}, error) {
	var array []interface{}
	if reader.buildValues {
		array = make([]interface{}, 0)
	}
	reader.offset++
	reader.skipWhitespace()
	if reader.body[reader.offset] == ']' {
		reader.offset++
		if !reader.buildValues {
			return nil, nil
		}
		return array, nil
	}
	for {
		value, err := reader.readValue(depth + 1)
		if err != nil {
			return nil, err
		}
		if reader.buildValues {
			array = append(array, value)
		}
		reader.skipWhitespace()
		if reader.body[reader.offset] == ']' {
			reader.offset++
			if !reader.buildValues {
				return nil, nil
			}
			return array, nil
		}
		reader.offset++ // 完整语法已验证，此处必然是元素分隔逗号。
	}
}

func (reader *strictJSONReader) readString() (string, error) {
	start := reader.offset
	escaped := reader.skipString()
	content := reader.body[start+1 : reader.offset-1]
	if !escaped && utf8.Valid(content) {
		return string(content), nil
	}
	// 标准库负责转义、代理项和无效 UTF-8 的替换规则；重复键比较
	// 必须基于解码后的字符串，不能将原始字节表示当作键的身份。
	var value string
	err := json.Unmarshal(reader.body[start:reader.offset], &value)
	return value, err
}

// skipString 只移动到已验证字符串的末尾，校验模式不需要复制普通值字符串。
func (reader *strictJSONReader) skipString() bool {
	reader.offset++
	escaped := false
	for {
		switch reader.body[reader.offset] {
		case '\\':
			escaped = true
			// 跳过反斜线及紧随的转义标记，防止转义引号被当成字符串结束。
			reader.offset += len(`\"`)
		case '"':
			reader.offset++
			return escaped
		default:
			reader.offset++
		}
	}
}

func (reader *strictJSONReader) skipWhitespace() {
	for reader.offset < len(reader.body) {
		switch reader.body[reader.offset] {
		case ' ', '\t', '\r', '\n':
			reader.offset++
		default:
			return
		}
	}
}

func requireJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return errors.New("JSON 请求体只能包含一个文档")
}
