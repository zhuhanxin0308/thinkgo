// Package seed 描述业务服务和负载器共同依赖的 PostgreSQL 种子数据合同。
package seed

import (
	"errors"
	"math"
)

const (
	priceBase    = 100
	priceStride  = 7919
	priceModulus = 199900
	// MaxPrice 是所有种子商品的最高可能单价，单位为分。
	MaxPrice = priceBase + priceModulus - 1
)

// ValidateProducts 校验 seed.sql 的 bigint 乘加和带符号余数，保留原有合法负种子。
func ValidateProducts(count, seed int64) error {
	// 数据集至少有两个商品。第一项被除数低于 1-base 时，若它因跨越负模周期而
	// 恰好仍有正价，第二项必落在同一周期的非正价格区间。
	if count < 2 || count > math.MaxInt64/priceStride || seed < 1-priceBase-priceStride ||
		seed > math.MaxInt64-count*priceStride {
		return errors.New("seed 与 products_per_tenant 无法生成正价的 PostgreSQL bigint 商品数据")
	}
	return nil
}

// Price 返回通过 ValidateProducts 校验的数据集中租户内商品的不可变单价。
func Price(localID, seed int64) int64 {
	return priceBase + (localID*priceStride+seed)%priceModulus
}
