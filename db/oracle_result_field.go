package db

import "strings"

// lookupModelScanColumn 优先按精确列名读取模型字段，顶层数据库行缺失时回退匹配 Oracle 大写列名。
func lookupModelScanColumn(row map[string]any, column string, projection bool) (any, bool) {
	if value, exists := row[column]; exists {
		return value, true
	}
	if !projection {
		if upper := strings.ToUpper(column); upper != column {
			if value, exists := row[upper]; exists {
				return value, true
			}
		}
	}
	return nil, false
}

// normalizeOracleModelRecordRow 为嵌入 *Model 的活动记录补齐由大写列返回的逻辑列键，
// 保持常规小写结果零额外分配，同时让主键身份、已加载字段集合与软删除状态在 Oracle 下一致生效。
func normalizeOracleModelRecordRow(mq *ModelQuery, metadata modelMetadata, raw map[string]any) map[string]any {
	if raw == nil || mq == nil || metadata.modelPath == nil {
		return raw
	}
	var normalized map[string]any
	copyIfUppercase := func(column string) {
		if column == "" {
			return
		}
		if _, exists := raw[column]; exists {
			return
		}
		upper := strings.ToUpper(column)
		if upper == column {
			return
		}
		value, exists := raw[upper]
		if !exists {
			return
		}
		if normalized == nil {
			normalized = make(map[string]any, len(raw)+4)
			for key, existing := range raw {
				normalized[key] = existing
			}
		}
		normalized[column] = value
	}
	for _, field := range metadata.fields {
		copyIfUppercase(field.column)
	}
	if mq.model != nil {
		copyIfUppercase(mq.model.primaryKeyField())
		if mq.model.softDelete {
			copyIfUppercase(mq.model.deleteTimeField)
		}
	}
	if mq.query != nil {
		copyIfUppercase(mq.query.insertPrimaryKey)
	}
	if normalized != nil {
		return normalized
	}
	return raw
}
