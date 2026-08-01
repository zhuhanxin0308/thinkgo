package db

import (
	"fmt"
	"reflect"
	"sync"
)

type modelFieldMetadata struct {
	index     int
	column    string
	omitEmpty bool
}

type modelMetadata struct {
	fields []modelFieldMetadata
}

type modelMetadataCacheEntry struct {
	metadata modelMetadata
	err      error
}

var modelMetadataCache sync.Map

func buildModelMetadata(typ reflect.Type) (modelMetadata, error) {
	if typ == nil || typ.Kind() != reflect.Struct {
		return modelMetadata{}, fmt.Errorf("%w: 元数据目标必须是结构体", ErrInvalidModel)
	}
	metadata := modelMetadata{fields: make([]modelFieldMetadata, 0, typ.NumField())}
	columns := make(map[string]string, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		if field.PkgPath != "" {
			continue
		}
		name, options := parseStructTag(field.Tag.Get("thinkgo"))
		if name == "-" {
			continue
		}
		if name == "" {
			name = ToSnakeCase(field.Name)
		}
		if err := validateIdentifier(name); err != nil {
			return modelMetadata{}, fmt.Errorf("%w: 字段 %s 的列名非法: %w", ErrInvalidModel, field.Name, err)
		}
		for option := range options {
			if option != "omitempty" {
				return modelMetadata{}, fmt.Errorf("%w: 字段 %s 使用未知标签选项 %q", ErrInvalidModel, field.Name, option)
			}
		}
		if previous, duplicated := columns[name]; duplicated {
			return modelMetadata{}, fmt.Errorf("%w: 字段 %s 与 %s 映射到重复列 %q", ErrInvalidModel, previous, field.Name, name)
		}
		columns[name] = field.Name
		metadata.fields = append(metadata.fields, modelFieldMetadata{index: index, column: name, omitEmpty: options["omitempty"]})
	}
	return metadata, nil
}

func loadModelMetadata(typ reflect.Type) (modelMetadata, error) {
	if cached, ok := modelMetadataCache.Load(typ); ok {
		entry := cached.(modelMetadataCacheEntry)
		return entry.metadata, entry.err
	}
	metadata, err := buildModelMetadata(typ)
	entry := modelMetadataCacheEntry{metadata: metadata, err: err}
	actual, _ := modelMetadataCache.LoadOrStore(typ, entry)
	stored := actual.(modelMetadataCacheEntry)
	return stored.metadata, stored.err
}

func cloneModelMetadata(metadata modelMetadata) modelMetadata {
	return modelMetadata{fields: append([]modelFieldMetadata(nil), metadata.fields...)}
}

// cachedModelMetadata exposes an isolated snapshot for validation and tests.
// Production conversion uses loadModelMetadata's immutable internal snapshot.
func cachedModelMetadata(typ reflect.Type) (modelMetadata, error) {
	metadata, err := loadModelMetadata(typ)
	if err != nil {
		return modelMetadata{}, err
	}
	return cloneModelMetadata(metadata), nil
}
