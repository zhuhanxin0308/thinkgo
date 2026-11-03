package binding

import (
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"strconv"
)

func isNumericRangeRule(rule string) bool {
	switch rule {
	case "gt", "lt", "egt", "elt", "min", "max", "between", "notBetween":
		return true
	default:
		return false
	}
}

// assignedFloatValue 将实际二进制浮点值表示为精确十进制，避免再次用最短
// 格式化舍入掩盖边界。分母为 2^n；乘以 5^n 后使用十进制指数，连最小
// float64 次正规数也保持在验证器的 1024 字节数值预算内。
func assignedFloatValue(plan *node, value reflect.Value) (json.Number, bool) {
	for plan.optional || value.Kind() == reflect.Pointer {
		if plan.optional {
			holder := value.Addr().Interface().(optionalValue)
			value = reflect.ValueOf(holder.valuePointer()).Elem()
		} else {
			if value.IsNil() {
				return "", false
			}
			value = value.Elem()
		}
		plan = plan.elem
	}
	if value.Kind() != reflect.Float32 && value.Kind() != reflect.Float64 {
		return "", false
	}
	rational := new(big.Rat).SetFloat64(value.Float())
	if rational == nil {
		return "", false
	}
	precision := rational.Denom().BitLen() - 1
	if precision == 0 {
		return json.Number(rational.Num().String()), true
	}
	factor := new(big.Int).Exp(big.NewInt(5), big.NewInt(int64(precision)), nil)
	coefficient := new(big.Int).Mul(rational.Num(), factor)
	return json.Number(coefficient.String() + "e-" + strconv.Itoa(precision)), true
}

// assignScalar 直接执行类型和范围检查，避免每个标量都经过 JSON 编码和再次解码。
func assignScalar(target reflect.Value, raw any) bool {
	switch target.Kind() {
	case reflect.String:
		value, ok := raw.(string)
		if ok {
			target.SetString(value)
		}
		return ok
	case reflect.Bool:
		value, ok := raw.(bool)
		if ok {
			target.SetBool(value)
		}
		return ok
	}
	text, valid := scalarNumber(raw)
	if !valid {
		return false
	}
	switch target.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value, err := strconv.ParseInt(text, 10, target.Type().Bits())
		if err != nil {
			return false
		}
		target.SetInt(value)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value, err := strconv.ParseUint(text, 10, target.Type().Bits())
		if err != nil {
			return false
		}
		target.SetUint(value)
	case reflect.Float32, reflect.Float64:
		value, err := strconv.ParseFloat(text, target.Type().Bits())
		if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
			return false
		}
		target.SetFloat(value)
	default:
		return false
	}
	return true
}

// scalarNumber 保留 JSON 数字原文；程序构造的数值按其真实位宽转成十进制，禁止隐式字符串转数值。
func scalarNumber(raw any) (string, bool) {
	if number, ok := raw.(json.Number); ok {
		text := string(number)
		return text, len(text) > 0 && (text[0] == '-' || text[0] >= '0' && text[0] <= '9') && json.Valid([]byte(text))
	}
	value := reflect.ValueOf(raw)
	if !value.IsValid() {
		return "", false
	}
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(value.Int(), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(value.Uint(), 10), true
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsInf(number, 0) || math.IsNaN(number) {
			return "", false
		}
		return strconv.FormatFloat(number, 'f', -1, value.Type().Bits()), true
	default:
		return "", false
	}
}
