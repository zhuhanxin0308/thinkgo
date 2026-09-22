package db

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"
)

var modelScanTimeType = reflect.TypeOf(time.Time{})

// assignModelScanValue 在独立字段上转换，Scanner 先写后报错也不会影响已有记录。
func assignModelScanValue(destination reflect.Value, source any) error {
	if !destination.IsValid() || !destination.CanSet() {
		return fmt.Errorf("模型字段不可写")
	}
	if valuer, ok := source.(driver.Valuer); ok {
		value := reflect.ValueOf(source)
		if value.Kind() == reflect.Pointer && value.IsNil() {
			source = nil
		} else {
			converted, err := valuer.Value()
			if err != nil {
				return err
			}
			if !driver.IsValue(converted) {
				return fmt.Errorf("驱动 Valuer 返回了不受支持的类型 %T", converted)
			}
			source = converted
		}
	}
	staged := reflect.New(destination.Type()).Elem()
	if err := convertModelScanValue(staged, source); err != nil {
		return err
	}
	destination.Set(staged)
	return nil
}

// convertModelScanValue 支持标准驱动标量、Scanner 和命名基础类型，拒绝隐式截断。
func convertModelScanValue(destination reflect.Value, source any) error {
	value := reflect.ValueOf(source)
	if value.IsValid() && value.Kind() == reflect.Pointer && value.IsNil() {
		source = nil
		value = reflect.Value{}
	}
	if destination.Kind() == reflect.Pointer {
		if source == nil {
			return nil
		}
		pointed := reflect.New(destination.Type().Elem())
		if err := convertModelScanValue(pointed.Elem(), source); err != nil {
			return err
		}
		destination.Set(pointed)
		return nil
	}
	if scanner, ok := destination.Addr().Interface().(sql.Scanner); ok {
		return scanner.Scan(cloneDatabaseValue(source))
	}
	if source == nil {
		switch destination.Kind() {
		case reflect.Interface:
			return nil
		case reflect.Slice:
			if destination.Type().Elem().Kind() == reflect.Uint8 {
				return nil
			}
		}
		return fmt.Errorf("NULL 不能映射到非空字段 %s", destination.Type())
	}
	if destination.Type() == modelScanTimeType {
		if value.Type() != modelScanTimeType {
			return fmt.Errorf("时间字段要求 time.Time，实际为 %T", source)
		}
		destination.Set(value)
		return nil
	}
	if destination.Kind() == reflect.Interface {
		cloned := reflect.ValueOf(cloneDatabaseValue(source))
		if !cloned.Type().AssignableTo(destination.Type()) {
			return fmt.Errorf("类型 %T 不实现目标接口 %s", source, destination.Type())
		}
		destination.Set(cloned)
		return nil
	}
	switch destination.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		text, err := modelScanScalarText(source)
		if err != nil {
			return err
		}
		integer, err := strconv.ParseInt(text, 10, destination.Type().Bits())
		if err != nil {
			return modelScanNumberError(err)
		}
		destination.SetInt(integer)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		text, err := modelScanScalarText(source)
		if err != nil {
			return err
		}
		integer, err := strconv.ParseUint(text, 10, destination.Type().Bits())
		if err != nil {
			return modelScanNumberError(err)
		}
		destination.SetUint(integer)
		return nil
	case reflect.Float32, reflect.Float64:
		text, err := modelScanScalarText(source)
		if err != nil {
			return err
		}
		number, err := strconv.ParseFloat(text, destination.Type().Bits())
		if err != nil {
			return modelScanNumberError(err)
		}
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return fmt.Errorf("浮点字段不接受非有限数值")
		}
		destination.SetFloat(number)
		return nil
	case reflect.Bool:
		if value.Kind() == reflect.Bool {
			destination.SetBool(value.Bool())
			return nil
		}
		if value.Kind() == reflect.String || value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8 {
			source, _ = modelScanScalarText(source)
		}
		converted, err := driver.Bool.ConvertValue(source)
		if err != nil {
			return fmt.Errorf("无法将 %T 转换为布尔值", source)
		}
		destination.SetBool(converted.(bool))
		return nil
	case reflect.String:
		text, err := modelScanScalarText(source)
		if err != nil {
			return err
		}
		destination.SetString(text)
		return nil
	case reflect.Slice:
		if destination.Type().Elem().Kind() == reflect.Uint8 {
			text, err := modelScanScalarText(source)
			if err != nil {
				return err
			}
			binary := reflect.MakeSlice(destination.Type(), len(text), len(text))
			for index := 0; index < len(text); index++ {
				binary.Index(index).SetUint(uint64(text[index]))
			}
			destination.Set(binary)
			return nil
		}
	}
	if value.Type().AssignableTo(destination.Type()) {
		destination.Set(reflect.ValueOf(cloneDatabaseValue(source)))
		return nil
	}
	return fmt.Errorf("不支持从 %T 映射到 %s", source, destination.Type())
}

// modelScanScalarText 仅接受驱动常用标量，禁止通过 fmt.Stringer 隐式解释复杂对象。
func modelScanScalarText(source any) (string, error) {
	if instant, ok := source.(time.Time); ok {
		return instant.Format(time.RFC3339Nano), nil
	}
	value := reflect.ValueOf(source)
	if !value.IsValid() {
		return "", fmt.Errorf("NULL 不能转换为文本标量")
	}
	switch value.Kind() {
	case reflect.String:
		return value.String(), nil
	case reflect.Bool:
		return strconv.FormatBool(value.Bool()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(value.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(value.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(value.Float(), 'g', -1, value.Type().Bits()), nil
	case reflect.Slice:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return string(value.Bytes()), nil
		}
	}
	return "", fmt.Errorf("类型 %T 不是可转换的驱动标量", source)
}

// modelScanNumberError 保留语法或范围错误，但不把数据库字段内容带入日志。
func modelScanNumberError(err error) error {
	if numberError, ok := err.(*strconv.NumError); ok {
		return fmt.Errorf("数字转换失败: %w", numberError.Err)
	}
	return err
}
