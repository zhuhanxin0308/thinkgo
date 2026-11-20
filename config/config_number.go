package config

import (
	"encoding/json"
	"math/big"
)

// configNumberToInt 使用精确有理数解析 JSON 数字，避免大整数经过 float64 舍入。
func configNumberToInt(number json.Number, minimum, maximum int64) (int, bool) {
	exact, ok := configExactNumber(number)
	if !ok {
		return 0, false
	}
	return configRationalToInt(exact, minimum, maximum)
}

// configRationalToInt 复用已精确解析的数值，避免环境覆盖再次展开大指数。
func configRationalToInt(exact *big.Rat, minimum, maximum int64) (int, bool) {
	if !exact.IsInt() || !exact.Num().IsInt64() {
		return 0, false
	}
	integer := exact.Num().Int64()
	if integer < minimum || integer > maximum {
		return 0, false
	}
	return int(integer), true
}

// configExactNumber 保留解析失败与非整数的区别，避免极端指数被误判为浮点原型。
func configExactNumber(number json.Number) (*big.Rat, bool) {
	return new(big.Rat).SetString(number.String())
}
