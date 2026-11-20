package seed

import (
	"math"
	"math/big"
	"testing"
)

// TestProductSeedArithmetic 使用任意精度的带符号余数独立校验 PostgreSQL bigint 合同。
func TestProductSeedArithmetic(t *testing.T) {
	for _, tc := range []struct {
		products, seed int64
	}{
		{2, -8018}, {25000, 20261107}, {3, 0}, {2, math.MaxInt64 - 15838},
		{math.MaxInt64 / 7919, 0}, {2, 199900 - 7919},
	} {
		if err := ValidateProducts(tc.products, tc.seed); err != nil {
			t.Fatal(err)
		}
		for _, local := range []int64{1, 2, tc.products} {
			sum := new(big.Int).Mul(big.NewInt(local), big.NewInt(7919))
			sum.Add(sum, big.NewInt(tc.seed))
			price := 100 + sum.Rem(sum, big.NewInt(199900)).Int64()
			if got := Price(local, tc.seed); got != price || got < 1 || got > MaxPrice {
				t.Errorf("第 %d 件、种子 %d 的价格为 %d，预期 %d", local, tc.seed, got, price)
			}
		}
	}
	for _, tc := range [][2]int64{{1, 1}, {2, -8019}, {2, math.MinInt64}, {2, math.MaxInt64}, {math.MaxInt64/7919 + 1, -8018}} {
		if err := ValidateProducts(tc[0], tc[1]); err == nil {
			t.Errorf("无法安全建库的商品数量与种子得到接受：%v", tc)
		}
	}
}
