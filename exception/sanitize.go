package exception

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"strings"
)

// sanitizeJSONBody 对调试页中的 JSON 递归脱敏，解析失败时由调用方隐藏整个请求体。
func sanitizeJSONBody(body string) (string, bool) {
	var payload interface{}
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return "", false
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return "", false
	}

	payload = sanitizeJSONValue("", payload)
	sanitizedBody, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	return string(sanitizedBody), true
}

// sanitizeXMLBody 保留 XML 结构，按元素和属性的本地名称识别敏感值。
func sanitizeXMLBody(body string) (string, bool) {
	decoder := xml.NewDecoder(strings.NewReader(body))
	var output strings.Builder
	encoder := xml.NewEncoder(&output)
	depth, seenRoot := 0, false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", false
		}
		switch node := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				if seenRoot {
					return "", false
				}
				seenRoot = true
			}
			for index := range node.Attr {
				attr := &node.Attr[index]
				if isSensitiveKey(attr.Name.Local) {
					attr.Value = redactedPlaceholder
				} else {
					attr.Value = sanitizeExceptionText(attr.Value)
				}
			}
			if isSensitiveKey(node.Name.Local) {
				if decoder.Skip() != nil || encoder.EncodeElement(redactedPlaceholder, node) != nil {
					return "", false
				}
				continue
			}
			token = node
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 {
				if strings.TrimSpace(string(node)) != "" {
					return "", false
				}
				continue
			}
			token = xml.CharData(sanitizeExceptionText(string(node)))
		default:
			// 注释、处理指令和文档声明可能含自由文本，不进入诊断输出。
			continue
		}
		if encoder.EncodeToken(token) != nil || output.Len() > maxDebugRequestBodyBytes {
			return "", false
		}
	}
	if !seenRoot || depth != 0 || encoder.Close() != nil || output.Len() > maxDebugRequestBodyBytes {
		return "", false
	}
	return output.String(), true
}

func sanitizeJSONValue(key string, value interface{}) interface{} {
	if isSensitiveKey(key) {
		return redactedPlaceholder
	}

	switch typed := value.(type) {
	case map[string]interface{}:
		sanitized := make(map[string]interface{}, len(typed))
		for innerKey, innerValue := range typed {
			sanitized[innerKey] = sanitizeJSONValue(innerKey, innerValue)
		}
		return sanitized
	case []interface{}:
		sanitized := make([]interface{}, len(typed))
		for index, item := range typed {
			sanitized[index] = sanitizeJSONValue(key, item)
		}
		return sanitized
	default:
		return value
	}
}

func isSensitiveKey(key string) bool {
	lowerKey := strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.TrimSpace(key)))
	if lowerKey == "" {
		return false
	}

	sensitiveKeys := []string{
		"authorization",
		"cookie",
		"setcookie",
		"password",
		"passwd",
		"token",
		"secret",
		"session",
		"apikey",
		"accesskey",
		"refreshtoken",
		"privatekey", "signingkey", "encryptionkey", "credential", "clientsecret",
	}

	for _, sensitiveKey := range sensitiveKeys {
		if lowerKey == sensitiveKey || strings.Contains(lowerKey, sensitiveKey) {
			return true
		}
	}
	return false
}
