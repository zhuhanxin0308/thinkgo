package db

import (
	"encoding"
	"fmt"
	"reflect"
)

var primaryKeyTextUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()

// PartialWriteError 表示写入已成功，但生成的主键无法回填到调用方模型。
// Result 保留持久化结果，便于调用方查询核对已写入的记录。
type PartialWriteError struct {
	Result InsertResult
	Cause  error
}

func (e *PartialWriteError) Error() string {
	if e == nil {
		return ErrPartialWrite.Error()
	}
	return fmt.Sprintf("%v: %v", ErrPartialWrite, e.Cause)
}

func (e *PartialWriteError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *PartialWriteError) Is(target error) bool {
	return target == ErrPartialWrite
}

type primaryKeyBinding struct {
	field     reflect.Value
	fieldName string
}

func (m *Model) preparePrimaryKey(value reflect.Value) (primaryKeyBinding, bool, error) {
	if err := m.validationError(); err != nil {
		return primaryKeyBinding{}, false, err
	}
	modelPrimaryKey := m.primaryKeyField()
	m.mu.RLock()
	primaryKeyExplicit := m.primaryKeyExplicit
	m.mu.RUnlock()
	storagePrimaryKey, err := resolveModelStoragePrimaryKey(m.db, modelPrimaryKey, primaryKeyExplicit)
	if err != nil {
		return primaryKeyBinding{}, false, err
	}
	caps := DriverCapabilities{InsertIDKinds: []InsertIDKind{InsertIDDynamic}}
	generatesPrimaryKey := true
	readCapabilities := func(connection Connection) error {
		if provider, ok := connection.(CapabilityProvider); ok {
			caps = provider.Capabilities()
		}
		if provider, ok := connection.(ModelPrimaryKeyGenerationProvider); ok {
			generatesPrimaryKey = provider.GeneratesStoragePrimaryKey(storagePrimaryKey)
		}
		return nil
	}
	m.mu.RLock()
	tx := m.transaction
	m.mu.RUnlock()
	if tx != nil {
		if err := tx.active(); err != nil {
			return primaryKeyBinding{}, false, err
		}
		err = readCapabilities(tx.connection)
	} else {
		err = m.db.WithConnection(readCapabilities)
	}
	if err != nil {
		return primaryKeyBinding{}, false, err
	}
	binding, primaryIsZero, err := preparePrimaryKeyBinding(value, modelPrimaryKey, caps)
	if err != nil {
		return primaryKeyBinding{}, false, err
	}
	if primaryIsZero && !generatesPrimaryKey {
		return primaryKeyBinding{}, false, fmt.Errorf("%w: storage backend does not generate explicit business primary key %q", ErrInvalidModel, modelPrimaryKey)
	}
	return binding, primaryIsZero, nil
}

func preparePrimaryKeyBinding(value reflect.Value, column string, caps DriverCapabilities) (primaryKeyBinding, bool, error) {
	field, fieldType, found, err := findModelColumn(value, column)
	if err != nil {
		return primaryKeyBinding{}, false, err
	}
	if !found || !field.CanSet() {
		return primaryKeyBinding{}, false, fmt.Errorf("%w: 主键 %q 不存在或不可写", ErrInvalidModel, column)
	}
	primaryIsZero := field.IsZero()
	if primaryIsZero && !supportedPrimaryKeyKind(field.Type(), caps.InsertIDKinds) {
		return primaryKeyBinding{}, false, fmt.Errorf("%w: 主键字段 %s 与驱动 ID 类型不兼容", ErrInvalidModel, fieldType.Name)
	}
	return primaryKeyBinding{field: field, fieldName: fieldType.Name}, primaryIsZero, nil
}

func findModelColumn(value reflect.Value, column string) (reflect.Value, reflect.StructField, bool, error) {
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return reflect.Value{}, reflect.StructField{}, false, fmt.Errorf("%w: 主键绑定目标必须是结构体", ErrInvalidModel)
	}
	metadata, err := loadModelMetadata(value.Type())
	if err != nil {
		return reflect.Value{}, reflect.StructField{}, false, err
	}
	for _, field := range metadata.fields {
		if field.column == column {
			return modelFieldValue(value, field.indexPath, true), value.Type().FieldByIndex(field.indexPath), true, nil
		}
	}
	return reflect.Value{}, reflect.StructField{}, false, nil
}

func supportedPrimaryKeyKind(fieldType reflect.Type, kinds []InsertIDKind) bool {
	if reflect.PointerTo(fieldType).Implements(primaryKeyTextUnmarshalerType) {
		return hasInsertIDKind(kinds, InsertIDObjectID, InsertIDString, InsertIDDynamic)
	}
	switch fieldType.Kind() {
	case reflect.Interface:
		return fieldType.NumMethod() == 0 && hasInsertIDKind(kinds, InsertIDInteger, InsertIDString, InsertIDObjectID, InsertIDDynamic)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return hasInsertIDKind(kinds, InsertIDInteger, InsertIDDynamic)
	case reflect.String:
		return hasInsertIDKind(kinds, InsertIDString, InsertIDObjectID, InsertIDDynamic)
	default:
		return false
	}
}

func hasInsertIDKind(kinds []InsertIDKind, accepted ...InsertIDKind) bool {
	for _, kind := range kinds {
		for _, candidate := range accepted {
			if kind == candidate {
				return true
			}
		}
	}
	return false
}

func (b primaryKeyBinding) Assign(id interface{}) error {
	if !b.field.IsValid() || !b.field.CanSet() {
		return fmt.Errorf("%w: 主键字段 %s 不可写", ErrInvalidModel, b.fieldName)
	}
	if id == nil {
		return fmt.Errorf("%w: 主键字段 %s 不能绑定 nil", ErrInvalidModel, b.fieldName)
	}

	idValue := reflect.ValueOf(id)
	if idValue.Type().AssignableTo(b.field.Type()) {
		b.field.Set(idValue)
		return nil
	}
	if b.field.Kind() == reflect.Interface && idValue.Type().Implements(b.field.Type()) {
		b.field.Set(idValue)
		return nil
	}
	if b.field.CanAddr() && b.field.Addr().Type().Implements(primaryKeyTextUnmarshalerType) {
		text, ok := id.(string)
		if !ok {
			return b.incompatible(id)
		}
		// 在临时值中解码，失败时不修改调用方模型的主键。
		target := reflect.New(b.field.Type())
		if err := target.Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(text)); err != nil {
			return fmt.Errorf("%w: 主键字段 %s 无法解码文本标识符: %v", ErrInvalidModel, b.fieldName, err)
		}
		b.field.Set(target.Elem())
		return nil
	}

	switch b.field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		integer, ok := databaseInteger(id)
		if !ok || !integer.IsInt64() || b.field.OverflowInt(integer.Int64()) {
			return b.incompatible(id)
		}
		b.field.SetInt(integer.Int64())
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		integer, ok := databaseInteger(id)
		if !ok || integer.Sign() < 0 || !integer.IsUint64() || b.field.OverflowUint(integer.Uint64()) {
			return b.incompatible(id)
		}
		b.field.SetUint(integer.Uint64())
		return nil
	case reflect.String:
		var text string
		switch typed := id.(type) {
		case string:
			text = typed
		case []byte:
			text = string(typed)
		case encoding.TextMarshaler:
			encoded, err := typed.MarshalText()
			if err != nil {
				return fmt.Errorf("%w: 主键字段 %s 无法编码文本标识符: %v", ErrInvalidModel, b.fieldName, err)
			}
			text = string(encoded)
		default:
			if idValue.Kind() != reflect.String {
				return b.incompatible(id)
			}
			text = idValue.String()
		}
		b.field.SetString(text)
		return nil
	default:
		return b.incompatible(id)
	}
}

func (b primaryKeyBinding) incompatible(id interface{}) error {
	return fmt.Errorf("%w: 主键字段 %s(%s) 无法绑定 %T", ErrInvalidModel, b.fieldName, b.field.Type(), id)
}
