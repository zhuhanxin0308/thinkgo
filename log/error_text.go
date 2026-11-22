package log

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/redact"
)

const visibleErrorCredentialRunes = 4

var (
	candidateErrorFieldPattern      = regexp.MustCompile(`([A-Za-z0-9_][A-Za-z0-9_.-]*)(["']?[\t ]*[:=][\t ]*)`)
	sensitiveErrorFieldSuffixRegexp = regexp.MustCompile(`(?i)(proxy-authorization|authorization|set-cookie|cookie|password|passwd|secret|session|token|api[-_]?key|refresh[-_]?token|private[-_]?key|signing[-_]?key|encryption[-_]?key|access[-_]?key|credential|client[-_]?secret)$`)
)

func findSensitiveErrorFieldMatches(message string) [][]int {
	candidates := candidateErrorFieldPattern.FindAllStringSubmatchIndex(message, -1)
	if len(candidates) == 0 {
		return nil
	}
	matches := make([][]int, 0, len(candidates))
	for _, sub := range candidates {
		rawKey := message[sub[2]:sub[3]]
		if field := sensitiveErrorField(rawKey); field != "" {
			matches = append(matches, []int{sub[3] - len(field), sub[1], sub[3] - len(field), sub[3]})
		}
	}
	return matches
}

func sensitiveErrorField(key string) string {
	if field := sensitiveErrorFieldSuffixRegexp.FindString(key); field != "" {
		return field
	}
	if redact.CredentialAlias(key) {
		return key
	}
	return ""
}

// redactErrorCredentials 按字段类型识别凭据边界，避免把认证方案或首项 Cookie 当成完整凭据。
func redactErrorCredentials(message string) string {
	return redactErrorCredentialsWithMask(message, maskErrorCredential)
}

// redactErrorCredentialsWithMask 复用同一边界解析，按日志或公开错误输出的策略遮蔽凭据。
func redactErrorCredentialsWithMask(message string, mask func(string) string) string {
	if !strings.ContainsAny(message, ":=") {
		return message
	}
	var result strings.Builder
	for len(message) > 0 {
		start := nextErrorJSONStart(message)
		plainEnd := redactPlainErrorText(&result, message, start, mask)
		if plainEnd == len(message) {
			break
		}
		if plainEnd > start {
			// 明文凭据与一个或多个对象重叠时，不能从中途恢复并暴露剩余成员。
			for start < plainEnd {
				_, length := redactErrorJSON(message[start:], mask)
				end := start + length
				if end >= plainEnd {
					plainEnd = end
					break
				}
				start = end + nextErrorJSONStart(message[end:])
			}
			message = message[plainEnd:]
			continue
		}
		fragment, end := redactErrorJSON(message[start:], mask)
		result.WriteString(fragment)
		message = message[start+end:]
	}
	return result.String()
}

// redactPlainErrorText 只在前段寻找字段，但允许已开始的凭据延伸到完整文本的边界。
func redactPlainErrorText(result *strings.Builder, message string, end int, mask func(string) string) int {
	position := 0
	for _, match := range findSensitiveErrorFieldMatches(message[:end]) {
		if match[0] < position {
			continue
		}
		result.WriteString(message[position:match[1]])
		field := strings.ToLower(message[match[2]:match[3]])
		value := message[match[1]:]
		length, quoted := errorCredentialLength(value, field)
		if match[1]+length > end {
			// 边界落入结构化片段，按一个完整的敏感值遮蔽，避免回写任何可疑字段。
			result.WriteString(logRedactedPlaceholder)
			return match[1] + length
		}
		content := value[:length]
		if quoted {
			result.WriteByte(content[0])
			content = content[1:]
			closed := len(content) > 0 && content[len(content)-1] == value[0] && errorQuoteEnd(value) == length
			if closed {
				content = content[:len(content)-1]
			}
			result.WriteString(maskErrorField(field, content, mask))
			if closed {
				result.WriteByte(value[0])
			}
		} else {
			if !isSensitiveHTTPErrorField(field) && strings.ContainsAny(content, ",;&") {
				// 模糊分隔符可能跟在短凭据后，不能按含分隔符的长度保留前缀。
				result.WriteString(logRedactedPlaceholder)
			} else {
				result.WriteString(maskErrorField(field, content, mask))
			}
		}
		position = match[1] + length
	}
	if position < end {
		result.WriteString(message[position:end])
	}
	return max(position, end)
}

// errorCredentialLength 保留有闭合引号的字段边界；不明确的 HTTP 敏感头延伸到行末。
func errorCredentialLength(value, field string) (int, bool) {
	if len(value) == 0 {
		return 0, false
	}
	if value[0] == '"' || value[0] == '\'' {
		if end := errorQuoteEnd(value); end > 0 {
			return end, true
		}
		return len(value), true
	}
	if isSensitiveHTTPErrorField(field) {
		if index := strings.IndexAny(value, "\r\n"); index >= 0 {
			return index, false
		}
		return len(value), false
	}
	// 逗号、分号和 & 可以出现在未经引用的凭据中，不能用作字段边界。
	if end := strings.IndexFunc(value, unicode.IsSpace); end >= 0 {
		return end, false
	}
	return len(value), false
}

func isSensitiveHTTPErrorField(field string) bool {
	return field == "authorization" || field == "proxy-authorization" || field == "cookie" || field == "set-cookie"
}

// errorQuoteEnd 跳过转义字符，只接受真正闭合的单引号或双引号。
func errorQuoteEnd(value string) int {
	for index := 1; index < len(value); index++ {
		if value[index] == '\\' {
			index++
		} else if value[index] == value[0] {
			return index + 1
		}
	}
	return 0
}

func maskErrorField(field, value string, mask func(string) string) string {
	switch field {
	case "authorization", "proxy-authorization":
		if separator := strings.IndexFunc(value, unicode.IsSpace); separator >= 0 {
			switch strings.ToLower(value[:separator]) {
			case "bearer", "basic", "digest", "negotiate", "ntlm":
				credential := strings.TrimLeftFunc(value[separator:], unicode.IsSpace)
				prefix := value[:len(value)-len(credential)]
				// token 型方案不包含空白，后接诊断文本不能把短凭据误判成长值。
				if !strings.EqualFold(value[:separator], "digest") {
					credential = unquotedErrorCredential(credential)
				}
				return prefix + mask(credential)
			}
		}
	case "cookie", "set-cookie":
		return maskErrorCookies(value, mask)
	}
	return mask(value)
}

// unquotedErrorCredential 提取单个不带引号的凭据，丢弃边界不明确的后续文本。
func unquotedErrorCredential(value string) string {
	value = strings.TrimLeftFunc(value, unicode.IsSpace)
	if end := strings.IndexFunc(value, unicode.IsSpace); end >= 0 {
		return value[:end]
	}
	return value
}

// maskErrorCredential 仅展示长凭据前四个字符，短值全部隐藏，并保持重复脱敏稳定。
func maskErrorCredential(value string) string {
	if value == "" {
		return value
	}
	if prefix, found := strings.CutSuffix(value, logRedactedPlaceholder); found && utf8.RuneCountInString(prefix) <= visibleErrorCredentialRunes {
		return value
	}
	count := 0
	for index := range value {
		if count == visibleErrorCredentialRunes {
			return value[:index] + logRedactedPlaceholder
		}
		count++
	}
	return logRedactedPlaceholder
}

// maskErrorCookies 逐项遮蔽 Cookie 值；引号内部的分号不构成新字段。
func maskErrorCookies(value string, mask func(string) string) string {
	var result strings.Builder
	start := 0
	var quote byte
	for index := 0; index <= len(value); index++ {
		if index < len(value) {
			character := value[index]
			if quote != 0 {
				if character == '\\' && index+1 < len(value) {
					index++
				} else if character == quote {
					quote = 0
				}
				continue
			}
			if character == '"' || character == '\'' {
				quote = character
				continue
			}
			if character != ';' {
				continue
			}
		}
		part := value[start:index]
		trimmed := strings.TrimLeftFunc(part, unicode.IsSpace)
		result.WriteString(part[:len(part)-len(trimmed)])
		if name, credential, found := strings.Cut(trimmed, "="); found {
			result.WriteString(name)
			result.WriteByte('=')
			if len(credential) > 1 && (credential[0] == '"' || credential[0] == '\'') && errorQuoteEnd(credential) == len(credential) {
				result.WriteByte(credential[0])
				result.WriteString(mask(credential[1 : len(credential)-1]))
				result.WriteByte(credential[0])
			} else {
				result.WriteString(mask(unquotedErrorCredential(credential)))
			}
		} else {
			result.WriteString(mask(trimmed))
		}
		if index < len(value) {
			result.WriteByte(';')
		}
		start = index + 1
	}
	return result.String()
}
