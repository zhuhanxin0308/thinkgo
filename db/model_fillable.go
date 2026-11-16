package db

import (
	"fmt"
	"strings"
)

// modelFillableColumns 配置完成后只读，派生模型可共享快照。
type modelFillableColumns map[string]struct{}

// SetFillable 设置调用方通过 map 提交的可写列。空名单拒绝所有外部列。
// 名称使用不带表前缀的实际列名，按大小写精确匹配。
func (m *Model) SetFillable(columns ...string) error {
	if m == nil {
		return ErrInvalidModel
	}
	fillable := make(modelFillableColumns, len(columns))
	for _, column := range columns {
		if err := validateIdentifier(column); err != nil || strings.Contains(column, ".") {
			return fmt.Errorf("%w: 白名单字段 %q 必须是单一列名", ErrInvalidModel, column)
		}
		if _, exists := fillable[column]; exists {
			return fmt.Errorf("%w: 白名单字段 %q 重复", ErrInvalidModel, column)
		}
		fillable[column] = struct{}{}
	}
	m.mu.Lock()
	m.fillable = fillable
	m.mu.Unlock()
	return nil
}

func (mq *ModelQuery) fillableColumns() modelFillableColumns {
	if mq == nil || mq.model == nil {
		return nil
	}
	mq.model.mu.RLock()
	defer mq.model.mu.RUnlock()
	return mq.model.fillable
}

func (columns modelFillableColumns) validate(data map[string]any) error {
	if columns == nil {
		return nil
	}
	for column := range data {
		if _, allowed := columns[column]; !allowed {
			return fmt.Errorf("%w: %q", ErrModelFieldNotFillable, column)
		}
	}
	return nil
}
