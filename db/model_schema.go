package db

// SchemaTable 返回配置钩子和数据库表前缀生效后的真实表名。
func (model *Model) SchemaTable() (string, error) {
	query := model.newModelQuery()
	if err := query.validationError(); err != nil {
		return "", err
	}
	return query.query.resolveTable(), nil
}
