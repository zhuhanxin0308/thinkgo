package openapi

import "strings"

// matchesIfNoneMatch 按 RFC 9110 的弱比较规则解析全部条件字段。
// 必须先确认完整列表合法再返回匹配，避免合法标签后的坏数据触发错误的 304。
func matchesIfNoneMatch(fields []string, etag string) bool {
	value := strings.Trim(strings.Join(fields, ","), " \t")
	if value == "*" {
		return true
	}
	matched := false
	for len(value) > 0 {
		// 列表接收方忽略空成员；引号内的逗号仍属于标签本身。
		value = strings.TrimLeft(value, " \t,")
		if value == "" {
			break
		}
		value = strings.TrimPrefix(value, "W/")
		if len(value) == 0 || value[0] != '"' {
			return false
		}
		end := 1
		for end < len(value) && value[end] != '"' {
			// etagc 允许可见字符及扩展字节，但不允许空格、控制字符或 DEL。
			const deleteCharacter = 0x7f
			if value[end] < '!' || value[end] == deleteCharacter {
				return false
			}
			end++
		}
		if end == len(value) {
			return false
		}
		matched = matched || value[:end+1] == etag
		value = strings.TrimLeft(value[end+1:], " \t")
		if len(value) > 0 && value[0] != ',' {
			return false
		}
	}
	return matched
}
