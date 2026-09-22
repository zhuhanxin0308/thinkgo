package log

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const visibleErrorCredentialRunes = 4

var sensitiveErrorFieldPattern = regexp.MustCompile(`(?i)(proxy-authorization|authorization|set-cookie|cookie|password|passwd|secret|session|token|api[-_]?key|refresh[-_]?token|private[-_]?key|signing[-_]?key|encryption[-_]?key|access[-_]?key|credential|client[-_]?secret)(["']?[\t ]*[:=][\t ]*)`)

// redactErrorCredentials 按字段类型识别凭据边界，避免把认证方案或首项 Cookie 当成完整凭据。
func redactErrorCredentials(message string) string {
	return redactErrorCredentialsWithMask(message, maskErrorCredential)
}

// redactErrorCredentialsWithMask 复用同一边界解析，按日志或公开错误输出的策略遮蔽凭据。
func redactErrorCredentialsWithMask(message string, mask func(string) string) string {
	matches := sensitiveErrorFieldPattern.FindAllStringSubmatchIndex(message, -1)
	if len(matches) == 0 {
		return message
	}
	var result strings.Builder
	position := 0
	for _, match := range matches {
		if match[0] < position {
			continue
		}
		result.WriteString(message[position:match[1]])
		field := strings.ToLower(message[match[2]:match[3]])
		value := message[match[1]:]
		length, quoted := errorCredentialLength(value, field)
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
			result.WriteString(maskErrorField(field, content, mask))
		}
		position = match[1] + length
	}
	result.WriteString(message[position:])
	return result.String()
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
	if field == "authorization" || field == "proxy-authorization" || field == "cookie" || field == "set-cookie" {
		for index := 0; index < len(value); index++ {
			if value[index] == '\r' || value[index] == '\n' {
				return index, false
			}
		}
		return len(value), false
	}
	if end := strings.IndexFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || strings.ContainsRune(",;&", character)
	}); end >= 0 {
		return end, false
	}
	return len(value), false
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
