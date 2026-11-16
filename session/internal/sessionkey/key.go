// Package sessionkey 定义内置会话驱动共用的存储键约束。
package sessionkey

// MaxBytes 是内置驱动接受的完整存储键长度上限。
const MaxBytes = 128

// ValidCharacters 检查存储键字符；允许空前缀，完整键的长度由调用方校验。
func ValidCharacters(key string) bool {
	for _, character := range key {
		isDigit := character >= '0' && character <= '9'
		isLower := character >= 'a' && character <= 'z'
		isUpper := character >= 'A' && character <= 'Z'
		if isDigit || isLower || isUpper || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}
