package db

import (
	"context"
	"fmt"
	"reflect"
)

// Find 查询一条记录并映射到非 nil 结构体指针；没有记录时返回 false，目标保持不变。
// 字段转换、记录绑定和上下文校验全部成功后，才会一次性发布新记录。
// 关联 DTO 字段使用 thinkgo:"关联名,readonly"，不会参与父记录的创建或更新。
func (mq *ModelQuery) Find(target any) (bool, error) {
	destination, metadata, err := modelScanDestination(target, false)
	if err != nil {
		return false, err
	}
	ctx, err := mq.modelScanContext()
	if err != nil {
		return false, err
	}
	row, raw, err := mq.findRecordRow()
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if row == nil {
		return false, nil
	}
	candidate := reflect.New(destination.Type()).Elem()
	candidate.Set(destination)
	if err := mapModelScanRow(ctx, candidate, row, metadata); err != nil {
		return false, err
	}
	if err := mq.bindScannedRecord(candidate, destination, raw); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	destination.Set(candidate)
	return true, nil
}

// Select 将查询结果映射到普通结构体切片或结构体指针切片。
// 嵌入 *Model 的业务记录只接受指针切片，避免 append 搬移值后保存操作仍指向旧记录。
// 空结果发布非 nil 空切片；任何行失败时，调用方切片及其原有元素保持不变。
func (mq *ModelQuery) Select(target any) error {
	destination, metadata, err := modelScanDestination(target, true)
	if err != nil {
		return err
	}
	ctx, err := mq.modelScanContext()
	if err != nil {
		return err
	}
	rows, rawRows, err := mq.selectRecordRows()
	if err != nil {
		return err
	}
	staged, err := mq.scanModelRows(ctx, destination.Type(), metadata, rows, rawRows)
	if err != nil {
		return err
	}
	destination.Set(staged)
	return nil
}

// scanModelRows 供列表、分页和批处理共用，全部转换成功后才交付独立切片。
func (mq *ModelQuery) scanModelRows(ctx context.Context, sliceType reflect.Type, metadata modelMetadata, rows, rawRows []map[string]any) (reflect.Value, error) {
	if len(rows) != len(rawRows) {
		return reflect.Value{}, fmt.Errorf("%w: 模型结果与原始行数量不一致", ErrInvalidDatabaseRow)
	}
	if err := ctx.Err(); err != nil {
		return reflect.Value{}, err
	}
	staged := reflect.MakeSlice(sliceType, len(rows), len(rows))
	for index, row := range rows {
		if err := ctx.Err(); err != nil {
			return reflect.Value{}, err
		}
		record := staged.Index(index)
		if record.Kind() == reflect.Pointer {
			record.Set(reflect.New(record.Type().Elem()))
			record = record.Elem()
		}
		candidate := reflect.New(record.Type()).Elem()
		if err := mapModelScanRow(ctx, candidate, row, metadata); err != nil {
			return reflect.Value{}, fmt.Errorf("第 %d 条模型结果映射失败: %w", index+1, err)
		}
		if err := mq.bindScannedRecord(candidate, record, rawRows[index]); err != nil {
			return reflect.Value{}, fmt.Errorf("第 %d 条模型记录绑定失败: %w", index+1, err)
		}
		record.Set(candidate)
	}
	if err := ctx.Err(); err != nil {
		return reflect.Value{}, err
	}
	return staged, nil
}

// modelScanContext 在执行前校验模型和取消状态，避免无效目标触发数据库访问。
func (mq *ModelQuery) modelScanContext() (context.Context, error) {
	if err := mq.validationError(); err != nil {
		return nil, err
	}
	ctx := mq.query.context()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return ctx, nil
}

// modelScanDestination 仅接受明确可写的目标形态，并复用读写一致的模型元数据。
func modelScanDestination(target any, multiple bool) (reflect.Value, modelMetadata, error) {
	value := reflect.ValueOf(target)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() {
		return reflect.Value{}, modelMetadata{}, fmt.Errorf("%w: 查询目标必须是非 nil 指针", ErrInvalidModel)
	}
	value = value.Elem()
	recordType := value.Type()
	if multiple {
		if value.Kind() != reflect.Slice {
			return reflect.Value{}, modelMetadata{}, fmt.Errorf("%w: 多条查询目标必须是结构体切片指针", ErrInvalidModel)
		}
		recordType = recordType.Elem()
		if recordType.Kind() == reflect.Pointer {
			recordType = recordType.Elem()
		}
	}
	if recordType.Kind() != reflect.Struct {
		return reflect.Value{}, modelMetadata{}, fmt.Errorf("%w: 查询结果元素必须是结构体", ErrInvalidModel)
	}
	metadata, err := loadModelMetadata(recordType)
	if err != nil {
		return reflect.Value{}, modelMetadata{}, err
	}
	if multiple && metadata.modelPath != nil && value.Type().Elem().Kind() != reflect.Pointer {
		return reflect.Value{}, modelMetadata{}, fmt.Errorf("%w: 嵌入 *db.Model 的记录必须使用指针切片，不能复制记录值", ErrInvalidModel)
	}
	if len(metadata.fields) == 0 {
		return reflect.Value{}, modelMetadata{}, fmt.Errorf("%w: 查询目标没有可映射字段", ErrInvalidModel)
	}
	for _, field := range metadata.fields {
		if field.readOnly {
			fieldType := modelFieldValue(reflect.Zero(recordType), field.indexPath, false).Type()
			if err := validateModelScanProjectionType(fieldType, make(map[reflect.Type]bool)); err != nil {
				return reflect.Value{}, modelMetadata{}, fmt.Errorf("只读字段 %q 类型无效: %w", field.column, err)
			}
		}
	}
	return value, metadata, nil
}

// mapModelScanRow 按稳定的字段顺序映射，未选择的字段保留原值，未知结果列允许忽略。
func mapModelScanRow(ctx context.Context, candidate reflect.Value, row map[string]any, metadata modelMetadata) error {
	return mapModelScanFields(ctx, candidate, row, metadata, false, make(map[uintptr]bool))
}

// mapModelScanFields 在只读投影内部递归转换，普通数据库字段保持严格的标量语义。
func mapModelScanFields(ctx context.Context, candidate reflect.Value, row map[string]any, metadata modelMetadata, projection bool, visiting map[uintptr]bool) error {
	if projection {
		identity := reflect.ValueOf(row).Pointer()
		if visiting[identity] {
			return fmt.Errorf("%w: 关联投影包含循环数据", ErrInvalidDatabaseRow)
		}
		visiting[identity] = true
		defer delete(visiting, identity)
	}
	matched := false
	for _, field := range metadata.fields {
		if err := ctx.Err(); err != nil {
			return err
		}
		source, exists := row[field.column]
		if !exists {
			continue
		}
		matched = true
		value, err := writableModelScanField(candidate, field.indexPath)
		if err != nil {
			return err
		}
		if projection || field.readOnly {
			err = assignModelScanProjection(ctx, value, source, visiting)
		} else {
			err = assignModelScanValue(value, source)
		}
		if err != nil {
			return fmt.Errorf("%w: 列 %q 无法映射到 %s: %w", ErrInvalidDatabaseRow, field.column, value.Type(), err)
		}
	}
	if !matched {
		return fmt.Errorf("%w: 结果行与目标结构体没有匹配字段", ErrInvalidDatabaseRow)
	}
	return ctx.Err()
}

// writableModelScanField 逐层复制匿名指针嵌入，保证失败时不会修改原目标引用的结构体。
func writableModelScanField(candidate reflect.Value, path []int) (reflect.Value, error) {
	if len(path) == 0 {
		return reflect.Value{}, fmt.Errorf("%w: 模型字段路径不能为空", ErrInvalidModel)
	}
	current := candidate
	for position, index := range path {
		if current.Kind() != reflect.Struct || index < 0 || index >= current.NumField() {
			return reflect.Value{}, fmt.Errorf("%w: 模型字段路径无效", ErrInvalidModel)
		}
		field := current.Field(index)
		if !field.CanSet() {
			return reflect.Value{}, fmt.Errorf("%w: 模型字段不可写", ErrInvalidModel)
		}
		if position == len(path)-1 {
			return field, nil
		}
		if field.Kind() == reflect.Pointer {
			if field.Type().Elem().Kind() != reflect.Struct {
				return reflect.Value{}, fmt.Errorf("%w: 嵌入字段必须指向结构体", ErrInvalidModel)
			}
			cloned := reflect.New(field.Type().Elem())
			if !field.IsNil() {
				cloned.Elem().Set(field.Elem())
			}
			field.Set(cloned)
			field = cloned.Elem()
		}
		current = field
	}
	return reflect.Value{}, fmt.Errorf("%w: 模型字段路径缺少终点", ErrInvalidModel)
}
