package exception

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/redact"
)

var xmlAssignmentKey = regexp.MustCompile(`([[:alnum:]_.-]+)["']?[[:space:]]*[:=]`)

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
	var text strings.Builder
	flushText := func() error {
		if text.Len() == 0 {
			return nil
		}
		sanitized := sanitizeXMLText(text.String())
		text.Reset()
		return encoder.EncodeToken(xml.CharData(sanitized))
	}
	type elementContent struct{ hasText, hasChild bool }
	var elements []elementContent
	seenRoot := false
	namespaces := make(map[string]string)
	const omittedMixedXML = "[MIXED XML BODY OMITTED]"
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
			if flushText() != nil {
				return "", false
			}
			if len(elements) == 0 {
				if seenRoot {
					return "", false
				}
				seenRoot = true
			} else {
				parent := &elements[len(elements)-1]
				if parent.hasText {
					return omittedMixedXML, true
				}
				parent.hasChild = true
			}
			node.Name = diagnosticXMLName(node.Name, namespaces)
			attributes := node.Attr[:0]
			for _, attr := range node.Attr {
				if attr.Name.Space == "xmlns" || attr.Name.Space == "" && attr.Name.Local == "xmlns" {
					continue
				}
				attr.Name = diagnosticXMLName(attr.Name, namespaces)
				if isSensitiveKey(attr.Name.Local) {
					attr.Value = redactedPlaceholder
				} else {
					attr.Value = sanitizeXMLText(attr.Value)
				}
				attributes = append(attributes, attr)
			}
			node.Attr = attributes
			if isSensitiveKey(node.Name.Local) {
				if decoder.Skip() != nil || encoder.EncodeElement(redactedPlaceholder, node) != nil {
					return "", false
				}
				continue
			}
			token = node
			elements = append(elements, elementContent{})
		case xml.EndElement:
			if flushText() != nil {
				return "", false
			}
			node.Name = diagnosticXMLName(node.Name, namespaces)
			token = node
			elements = elements[:len(elements)-1]
		case xml.CharData:
			significant := strings.TrimSpace(string(node)) != ""
			if len(elements) == 0 {
				if significant {
					return "", false
				}
				continue
			}
			if significant {
				current := &elements[len(elements)-1]
				if current.hasChild {
					return omittedMixedXML, true
				}
				current.hasText = true
			}
			// 相邻正文可能被 CDATA 等结构拆片，合并后再识别凭据。
			text.Write(node)
			continue
		default:
			// 注释、处理指令和文档声明可能含自由文本，不进入诊断输出。
			continue
		}
		if encoder.EncodeToken(token) != nil || output.Len() > maxDebugRequestBodyBytes {
			return "", false
		}
	}
	if !seenRoot || len(elements) != 0 || encoder.Close() != nil || output.Len() > maxDebugRequestBodyBytes {
		return "", false
	}
	return output.String(), true
}

func sanitizeXMLText(value string) string {
	// 先识别凭据键，即使实体解码后的换行让通用文本脱敏器判定值为空。
	for _, assignment := range xmlAssignmentKey.FindAllStringSubmatch(value, -1) {
		if isSensitiveKey(assignment[1]) {
			return redactedPlaceholder
		}
	}
	sanitized := sanitizeExceptionText(value)
	// XML 实体展开可能把凭据中间的字符变为空白，识别到凭据后隐藏整个值。
	if strings.Contains(sanitized, redactedPlaceholder) {
		return redactedPlaceholder
	}
	return sanitized
}

// diagnosticXMLName 保留命名空间之间的区分，防止编码器重新暴露原始 URI。
func diagnosticXMLName(name xml.Name, namespaces map[string]string) xml.Name {
	if name.Space == "" || name.Space == "http://www.w3.org/XML/1998/namespace" {
		return name
	}
	namespace, exists := namespaces[name.Space]
	if !exists {
		namespace = "urn:thinkgo:debug:namespace:" + strconv.Itoa(len(namespaces)+1)
		namespaces[name.Space] = namespace
	}
	name.Space = namespace
	return name
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
		"privatekey", "signingkey", "encryptionkey", "clientsecret",
	}

	for _, sensitiveKey := range sensitiveKeys {
		if lowerKey == sensitiveKey || strings.Contains(lowerKey, sensitiveKey) {
			return true
		}
	}
	return redact.CredentialAlias(key)
}
