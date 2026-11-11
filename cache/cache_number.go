package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidCacheValue 表示缓存值无法按公开的编码合同无损保存。
var ErrInvalidCacheValue = errors.New("缓存值编码无效")

// validateCacheNumber 在写入前拒绝非法数字文本，所有后端保留相同失败语义。
func validateCacheNumber(value interface{}) error {
	number, ok := value.(json.Number)
	if !ok {
		return nil
	}
	if number.String() == "" {
		return fmt.Errorf("%w: JSON 数字不能为空", ErrInvalidCacheValue)
	}
	if _, err := json.Marshal(number); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCacheValue, err)
	}
	return nil
}

// validCacheJSONInteger 只验证 JSON 整数语法，不把数值限制到机器整数范围。
func validCacheJSONInteger(text string) bool {
	return !strings.ContainsAny(text, ".eE") && validateCacheNumber(json.Number(text)) == nil
}
