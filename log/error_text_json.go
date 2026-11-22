package log

import (
	"bytes"
	"encoding/json"
	"strings"
)

const errorJSONSpace = " \t\r\n"

// nextErrorJSONStart 检查对象和数组的第一个成员，区分 JSON、时间戳与普通日志标签。
func nextErrorJSONStart(message string) int {
	for offset := 0; offset < len(message); {
		index := strings.IndexAny(message[offset:], "{[")
		if index < 0 {
			break
		}
		start := offset + index
		probe := start
		for probe < len(message) && message[probe] == '[' {
			probe = len(message) - len(strings.TrimLeft(message[probe+1:], errorJSONSpace))
		}
		if probe == len(message) {
			break
		}
		if message[probe] == '{' {
			rest := strings.TrimLeft(message[probe+1:], errorJSONSpace)
			if strings.HasPrefix(rest, `"`) || strings.HasPrefix(rest, "}") {
				return start
			}
		} else if probe > start && startsErrorJSONArrayValue(message[probe:]) {
			return start
		}
		offset = probe + 1
	}
	return len(message)
}

func startsErrorJSONArrayValue(rest string) bool {
	switch rest[0] {
	case '"', ']':
		return true
	case 't', 'f', 'n':
		for _, literal := range []string{"true", "false", "null"} {
			if tail, ok := strings.CutPrefix(rest, literal); ok {
				rest = tail
				break
			}
		}
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		end := 0
		for end < len(rest) && strings.ContainsRune("0123456789+-.eE", rune(rest[end])) {
			end++
		}
		if !json.Valid([]byte(rest[:min(end, maxLogContextBytes)])) {
			return false
		}
		rest = rest[end:]
	default:
		return false
	}
	rest = strings.TrimLeft(rest, errorJSONSpace)
	return strings.HasPrefix(rest, ",") || strings.HasPrefix(rest, "]")
}

// errorJSONRedactor 按解码后的原键分类，保留无需脱敏的原文、数字精度和重复字段。
type errorJSONRedactor struct {
	source  string
	decoder *json.Decoder
	mask    func(string) string
	nodes   int
	copied  int
	output  strings.Builder
}

func redactErrorJSON(message string, mask func(string) string) (string, int) {
	source := message[:min(len(message), maxLogContextBytes)]
	s := errorJSONRedactor{source: source, decoder: json.NewDecoder(strings.NewReader(source)), mask: mask}
	s.decoder.UseNumber()
	if !s.value(1, "", true) {
		// 缺少可信结束边界时，保留候选片段之前的文本，省略整个未确认的余段。
		return logTruncatedPlaceholder, len(message)
	}
	end := int(s.decoder.InputOffset())
	if s.copied > 0 {
		s.output.WriteString(source[s.copied:end])
		source = s.output.String()
	} else {
		source = source[:end]
	}
	if strings.ContainsAny(source, "\t\r\n") {
		// JSON 结构中的真实空白不能交给日志行转义，否则再次读取时会落入错误回退。
		var compact bytes.Buffer
		if json.Compact(&compact, []byte(source)) != nil {
			return logTruncatedPlaceholder, end
		}
		source = compact.String()
	}
	if len(source) > maxLogContextBytes {
		return logTruncatedPlaceholder, end
	}
	return source, end
}

func (s *errorJSONRedactor) value(depth int, field string, visible bool) bool {
	if depth > maxLogContextDepth || s.nodes >= maxLogContextNodes {
		return false
	}
	s.nodes++
	before := int(s.decoder.InputOffset())
	token, err := s.decoder.Token()
	if err != nil {
		return false
	}
	end := int(s.decoder.InputOffset())
	raw := strings.TrimLeft(s.source[before:end], errorJSONSpace+",:")
	start := end - len(raw)
	replacement := logRedactedPlaceholder
	switch v := token.(type) {
	case json.Delim:
		for s.decoder.More() {
			child := ""
			if v == '{' {
				keyBefore := int(s.decoder.InputOffset())
				key, err := s.decoder.Token()
				name, ok := key.(string)
				if err != nil || !ok {
					return false
				}
				if visible && field == "" {
					child = strings.ToLower(sensitiveErrorField(name))
					s.redactDiagnosticText(keyBefore, int(s.decoder.InputOffset()), name)
				}
			}
			if !s.value(depth+1, child, visible && field == "") {
				return false
			}
		}
		if _, err := s.decoder.Token(); err != nil {
			return false
		}
		end = int(s.decoder.InputOffset())
	case string:
		if visible && field == "" {
			s.redactDiagnosticText(start, end, v)
		} else if visible && field != "" {
			replacement = maskErrorField(field, v, s.mask)
		}
	default:
		if visible && field != "" {
			replacement = s.mask(raw)
		}
	}
	if visible && field != "" {
		s.replace(start, end, replacement)
	}
	return true
}

// redactDiagnosticText 对属性名和普通字符串值中的诊断文本沿用同一明文策略。
func (s *errorJSONRedactor) redactDiagnosticText(before, end int, value string) {
	if !strings.ContainsAny(value, ":=") {
		return
	}
	var plain strings.Builder
	redactPlainErrorText(&plain, value, len(value), s.mask)
	if cleaned := plain.String(); cleaned != value {
		// Token 已确认当前为字符串；保留起始引号之前的原始逗号、冒号与空白。
		start := before + strings.IndexByte(s.source[before:end], '"')
		s.replace(start, end, cleaned)
	}
}

func (s *errorJSONRedactor) replace(start, end int, value string) {
	s.output.WriteString(s.source[s.copied:start])
	encoded, _ := json.Marshal(value)
	s.output.Write(encoded)
	s.copied = end
}
