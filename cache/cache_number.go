package cache

import "github.com/zhuhanxin0308/thinkgo/v3/cache/contract"

// ErrInvalidCacheValue 表示缓存值无法按公开的编码合同无损保存。
var ErrInvalidCacheValue = contract.ErrInvalidCacheValue

// validateCacheNumber 在写入前拒绝非法数字文本，所有后端保留相同失败语义。
func validateCacheNumber(value interface{}) error {
	return contract.ValidateCacheNumber(value)
}
