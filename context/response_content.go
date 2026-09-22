package context

import (
	"fmt"
	"reflect"
	"strconv"
)

func responseContentString(content interface{}) (text string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			text = ""
			err = fmt.Errorf("%w: 内容字符串化失败", ErrResponseSerialization)
		}
	}()
	if content == nil {
		return "", nil
	}
	if bytes, ok := content.([]byte); ok {
		return string(bytes), nil
	}
	if stringer, ok := content.(fmt.Stringer); ok {
		return stringer.String(), nil
	}
	value := reflect.ValueOf(content)
	switch value.Kind() {
	case reflect.String:
		return value.String(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(value.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(value.Uint(), 10), nil
	case reflect.Float32:
		return strconv.FormatFloat(value.Float(), 'g', -1, 32), nil
	case reflect.Float64:
		return strconv.FormatFloat(value.Float(), 'g', -1, 64), nil
	default:
		return "", fmt.Errorf("%w: Content 不支持类型 %T", ErrResponseSerialization, content)
	}
}
