package context

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
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
	if _, err := decodeStrictJSONValue(body); err != nil {
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

func decodeStrictJSONValue(body []byte) (interface{}, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("JSON 请求体不能为空")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := readJSONValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	return value, nil
}

func readJSONValue(decoder *json.Decoder, depth int) (interface{}, error) {
	if depth > maxJSONNestingDepth {
		return nil, fmt.Errorf("JSON 嵌套深度不能超过 %d", maxJSONNestingDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}

	switch delimiter {
	case '{':
		object := make(map[string]interface{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return nil, keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("JSON 对象键必须是字符串")
			}
			if _, duplicated := object[key]; duplicated {
				return nil, fmt.Errorf("JSON 对象包含重复键 %q", key)
			}
			value, valueErr := readJSONValue(decoder, depth+1)
			if valueErr != nil {
				return nil, valueErr
			}
			object[key] = value
		}
		if _, err = decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		array := make([]interface{}, 0)
		for decoder.More() {
			value, valueErr := readJSONValue(decoder, depth+1)
			if valueErr != nil {
				return nil, valueErr
			}
			array = append(array, value)
		}
		if _, err = decoder.Token(); err != nil {
			return nil, err
		}
		return array, nil
	default:
		return nil, fmt.Errorf("JSON 分隔符 %q 位置非法", delimiter)
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
