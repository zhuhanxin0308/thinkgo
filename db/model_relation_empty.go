package db

// initializeEmptyRelation 保留已请求关联的字段和类型，即使整批没有可查询的关联键。
func initializeEmptyRelation(rows []map[string]interface{}, relation RelationDefinition) {
	for _, row := range rows {
		switch relation.Type {
		case relationHasOne, relationBelongsTo:
			row[relation.Name] = map[string]interface{}(nil)
		case relationHasMany, relationBelongsToMany:
			row[relation.Name] = []map[string]interface{}{}
		}
	}
}
