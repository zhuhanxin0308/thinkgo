package db

import "strings"

func (q *Query) isOracleDialect() bool {
	if q == nil {
		return false
	}
	if b := q.builder(); b != nil {
		return strings.EqualFold(b.DialectName(), "oracle")
	}
	if q.db != nil {
		return strings.EqualFold(q.db.DialectName(), "oracle")
	}
	return false
}

func (mq *ModelQuery) isOracleDialect() bool {
	if mq == nil {
		return false
	}
	if mq.query != nil && mq.query.isOracleDialect() {
		return true
	}
	if mq.model != nil && mq.model.db != nil {
		return strings.EqualFold(mq.model.db.DialectName(), "oracle")
	}
	return false
}

// lookupModelScanColumn 优先按精确列名读取模型字段，仅在 Oracle 方言顶层数据库行缺失时回退匹配大写列名。
func lookupModelScanColumn(row map[string]any, column string, projection bool, oracleDialect bool) (any, bool) {
	if value, exists := row[column]; exists {
		return value, true
	}
	if !projection && oracleDialect {
		if upper := strings.ToUpper(column); upper != column {
			if value, exists := row[upper]; exists {
				return value, true
			}
		}
	}
	return nil, false
}

// normalizeOracleModelRecordRow 为 Oracle 连接上嵌入 *Model 的活动记录补齐由大写列返回的逻辑列键，
// 非 Oracle 方言直接返回原始行，避免大小写敏感别名绕过缺失主键守卫。
func normalizeOracleModelRecordRow(mq *ModelQuery, metadata modelMetadata, raw map[string]any) map[string]any {
	if raw == nil || !mq.isOracleDialect() || metadata.modelPath == nil {
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
