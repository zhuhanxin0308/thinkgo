package db

import (
	"context"
	"fmt"
	"reflect"
)

// validateModelScanProjectionType 在查询前检查所有 DTO 层级，禁止关联携带可保存的 Model。
// 类型本身可以递归；实际数据的循环由映射路径检测。
func validateModelScanProjectionType(typ reflect.Type, visited map[reflect.Type]bool) error {
	if isModelScalar(typ) || visited[typ] {
		return nil
	}
	visited[typ] = true
	if typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
		return validateModelScanProjectionType(typ.Elem(), visited)
	}
	if typ.Kind() != reflect.Struct {
		return nil
	}
	metadata, err := loadModelMetadata(typ)
	if err != nil {
		return err
	}
	if typ == reflect.TypeFor[Model]() || metadata.modelPath != nil {
		return fmt.Errorf("%w: 只读关联 DTO 不能嵌入 *db.Model，请单独查询需要保存的关联记录", ErrInvalidModel)
	}
	for _, field := range metadata.fields {
		fieldType := modelFieldValue(reflect.Zero(typ), field.indexPath, false).Type()
		if err := validateModelScanProjectionType(fieldType, visited); err != nil {
			return err
		}
	}
	return nil
}

// assignModelScanProjection 将关联对象和数组转换到临时 DTO；嵌套标量复用严格转换规则。
func assignModelScanProjection(ctx context.Context, destination reflect.Value, source any, visiting map[uintptr]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	typ := destination.Type()
	if isModelScalar(typ) {
		return assignModelScanValue(destination, source)
	}
	if typ.Kind() == reflect.Pointer {
		if source == nil {
			destination.SetZero()
			return nil
		}
		if row, ok := source.(map[string]any); ok && row == nil {
			destination.SetZero()
			return nil
		}
		staged := reflect.New(typ.Elem())
		if err := assignModelScanProjection(ctx, staged.Elem(), source, visiting); err != nil {
			return err
		}
		destination.Set(staged)
		return nil
	}
	if typ.Kind() == reflect.Struct {
		row, ok := source.(map[string]any)
		if !ok || row == nil {
			return fmt.Errorf("关联对象要求非空数据行，实际为 %T；可空单关系请使用 DTO 指针", source)
		}
		metadata, err := loadModelMetadata(typ)
		if err != nil {
			return err
		}
		staged := reflect.New(typ).Elem()
		if err := mapModelScanFields(ctx, staged, row, metadata, true, visiting); err != nil {
			return err
		}
		destination.Set(staged)
		return nil
	}
	if typ.Kind() == reflect.Slice {
		elementType := typ.Elem()
		if elementType.Kind() == reflect.Pointer {
			elementType = elementType.Elem()
		}
		if elementType.Kind() == reflect.Struct && !isModelScalar(typ.Elem()) {
			rows, ok := source.([]map[string]any)
			if source != nil && !ok {
				return fmt.Errorf("关联数组要求数据行切片，实际为 %T", source)
			}
			staged := reflect.MakeSlice(typ, len(rows), len(rows))
			for index, row := range rows {
				if err := assignModelScanProjection(ctx, staged.Index(index), row, visiting); err != nil {
					return fmt.Errorf("第 %d 个关联元素转换失败: %w", index+1, err)
				}
			}
			destination.Set(staged)
			return nil
		}
	}
	return assignModelScanValue(destination, source)
}
