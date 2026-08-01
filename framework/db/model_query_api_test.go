package db

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type modelBusinessConnection struct {
	connectionIdentityState
	rows              []map[string]interface{}
	tableRows         map[string][]map[string]interface{}
	count             int64
	aggregate         interface{}
	insertID          int64
	updateCount       int64
	deleteCount       int64
	insertData        map[string]interface{}
	updateData        map[string]interface{}
	lastContext       context.Context
	lastRawSQL        string
	lastRawArgs       []interface{}
	lastTable         string
	lastWhere         []string
	lastWhereArgs     []interface{}
	lastFieldsByTable map[string]string
}

func (connection *modelBusinessConnection) sourceRows(table string) []map[string]interface{} {
	if rows, exists := connection.tableRows[table]; exists {
		return rows
	}
	return connection.rows
}

func (connection *modelBusinessConnection) selectedRows(table, fields string, where []string, args []interface{}, limit, offset int) []map[string]interface{} {
	connection.lastTable = table
	if connection.lastFieldsByTable == nil {
		connection.lastFieldsByTable = make(map[string]string)
	}
	connection.lastFieldsByTable[table] = fields
	connection.lastWhere = append([]string(nil), where...)
	connection.lastWhereArgs = append([]interface{}(nil), args...)
	if strings.Contains(fields, " AS tp_aggregate") {
		return []map[string]interface{}{{"tp_aggregate": connection.aggregate}}
	}
	rows := cloneDatabaseRows(connection.sourceRows(table))
	if offset >= len(rows) {
		return []map[string]interface{}{}
	}
	if offset > 0 {
		rows = rows[offset:]
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	if fields == "" || fields == "*" {
		return rows
	}
	projected := make([]map[string]interface{}, len(rows))
	fieldNames := strings.Split(fields, ",")
	for index, row := range rows {
		projected[index] = make(map[string]interface{}, len(fieldNames))
		for _, rawField := range fieldNames {
			field := strings.TrimSpace(rawField)
			if value, exists := row[field]; exists {
				projected[index][field] = value
			}
		}
	}
	return projected
}

func (connection *modelBusinessConnection) Select(ctx context.Context, request SelectRequest) ([]map[string]interface{}, error) {
	connection.lastContext = ctx
	where, args, err := request.Predicate().compileSQL()
	if err != nil {
		return nil, err
	}
	if request.Aggregate() != nil {
		connection.lastTable = request.Table()
		connection.lastWhere = append([]string(nil), where...)
		connection.lastWhereArgs = append([]interface{}(nil), args...)
		return []map[string]interface{}{{"tp_aggregate": connection.aggregate}}, nil
	}
	return connection.selectedRows(request.Table(), request.Fields(), where, args, request.Limit(), request.Offset()), nil
}

func (connection *modelBusinessConnection) Insert(ctx context.Context, request InsertRequest) (InsertResult, error) {
	connection.lastContext = ctx
	connection.insertData = request.Data()
	return InsertResult{Affected: 1, ID: connection.insertID, IDKnown: request.WantsID(), Data: request.Data()}, nil
}

func (connection *modelBusinessConnection) Update(ctx context.Context, request UpdateRequest) (UpdateResult, error) {
	connection.lastContext = ctx
	where, args, err := request.Predicate().compileSQL()
	if err != nil {
		return UpdateResult{}, err
	}
	connection.updateData = request.Data()
	connection.lastWhere = append([]string(nil), where...)
	connection.lastWhereArgs = append([]interface{}(nil), args...)
	return UpdateResult{Affected: connection.updateCount, Data: request.Data()}, nil
}

func (connection *modelBusinessConnection) Delete(ctx context.Context, request DeleteRequest) (DeleteResult, error) {
	connection.lastContext = ctx
	where, args, err := request.Predicate().compileSQL()
	if err != nil {
		return DeleteResult{}, err
	}
	connection.lastWhere = append([]string(nil), where...)
	connection.lastWhereArgs = append([]interface{}(nil), args...)
	return DeleteResult{Deleted: connection.deleteCount}, nil
}

func (connection *modelBusinessConnection) Count(ctx context.Context, _ CountRequest) (int64, error) {
	connection.lastContext = ctx
	return connection.count, nil
}

func (connection *modelBusinessConnection) Query(sqlText string, args ...interface{}) ([]map[string]interface{}, error) {
	connection.lastRawSQL = sqlText
	connection.lastRawArgs = append([]interface{}(nil), args...)
	if strings.Contains(sqlText, "SELECT COUNT(*) AS tg_count") {
		return []map[string]interface{}{{"tg_count": connection.count}}, nil
	}
	return cloneDatabaseRows(connection.rows), nil
}

func (connection *modelBusinessConnection) QueryContext(ctx context.Context, sqlText string, args ...interface{}) ([]map[string]interface{}, error) {
	connection.lastContext = ctx
	return connection.Query(sqlText, args...)
}

func (connection *modelBusinessConnection) Execute(sqlText string, args ...interface{}) (int64, error) {
	connection.lastRawSQL = sqlText
	connection.lastRawArgs = append([]interface{}(nil), args...)
	return connection.updateCount, nil
}

func (connection *modelBusinessConnection) ExecuteContext(ctx context.Context, sqlText string, args ...interface{}) (int64, error) {
	connection.lastContext = ctx
	return connection.Execute(sqlText, args...)
}

func (connection *modelBusinessConnection) Close() error { return nil }

// TestModelQueryProxiesBuildCompleteSafeQuery 验证模型层每个链式代理都把状态准确传给 Query，
// 并能共同构造参数化的高级查询而不丢失上下文或覆盖既有条件。
func TestModelQueryProxiesBuildCompleteSafeQuery(t *testing.T) {
	database := NewDB(&mockConnection{})
	model := NewModel(database, "users")
	ctx := context.WithValue(context.Background(), struct{ name string }{"request"}, "trace-7")
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mq := model.newModelQuery().
		WithContext(ctx).
		Where("users.status = ?", 1).
		WhereOr("users.priority = ?", 2).
		WhereColumn("users.created_by", "=", "users.updated_by").
		WhereExp("users.score", ">", "users.baseline + ?", 5).
		WhereTime("users.created_at", "between", start, end).
		WhereField("users.tenant_id", "=", 9).
		WhereMap(map[string]interface{}{"users.enabled": true}).
		WhereFields([][]interface{}{{"users.age", ">=", 18}}).
		WhereIn("users.id", []interface{}{1, 2}).
		WhereNotIn("users.kind", []interface{}{"bot"}).
		WhereLike("users.name", "A%").
		WhereNull("users.archived_at").
		WhereNotNull("users.email").
		WhereBetween("users.level", 1, 5).
		WhereRaw("users.region_id = ?", 3).
		Limit(20).
		Offset(4).
		Page(2, 10).
		Order("users.id DESC").
		Field("users.id, users.name").
		Group("users.id, users.name").
		Having("users.id > ?", 0).
		HavingField("users.id", "<", 100).
		Distinct().
		Join("roles", "users.role_id = roles.id").
		LeftJoin("teams", "users.team_id = teams.id").
		RightJoin("departments", "users.department_id = departments.id").
		Lock(false)

	if mq.query.ctx != ctx || mq.query.limit != 10 || mq.query.offset != 10 {
		t.Fatalf("模型查询上下文或分页代理错误: ctx=%v limit=%d offset=%d", mq.query.ctx, mq.query.limit, mq.query.offset)
	}
	if len(mq.query.where) != 14 || len(mq.query.joins) != 3 || !mq.query.distinct {
		t.Fatalf("模型查询条件或连接代理丢失: where=%v joins=%v distinct=%v", mq.query.where, mq.query.joins, mq.query.distinct)
	}
	sqlText, args, err := mq.query.BuildSelectSQL()
	if err != nil {
		t.Fatalf("完整模型查询构造失败: %v", err)
	}
	for _, fragment := range []string{
		"SELECT DISTINCT users.id, users.name FROM users",
		"JOIN roles ON users.role_id = roles.id",
		"LEFT JOIN teams ON users.team_id = teams.id",
		"RIGHT JOIN departments ON users.department_id = departments.id",
		"GROUP BY users.id, users.name",
		"HAVING users.id > ? AND users.id < ?",
		"ORDER BY users.id DESC LIMIT 10 OFFSET 10 LOCK IN SHARE MODE",
	} {
		if !strings.Contains(sqlText, fragment) {
			t.Fatalf("完整模型 SQL 缺少 %q，实际为 %q", fragment, sqlText)
		}
	}
	if len(args) != len(mq.query.args)+len(mq.query.havingArgs) {
		t.Fatalf("构造 SQL 时参数数量不一致: sql=%d query=%d", len(args), len(mq.query.args)+len(mq.query.havingArgs))
	}

	rawHaving := model.newModelQuery().Group("tenant_id").HavingRaw("COUNT(id) > ?", 2)
	if _, _, err := rawHaving.query.BuildSelectSQL(); err != nil {
		t.Fatalf("HavingRaw 代理失败: %v", err)
	}
	expressions := model.newModelQuery().WhereField("id", "=", 1).Inc("score", 2).Dec("quota", 3)
	if len(expressions.query.setExprs) != 2 || expressions.query.setExprs[0].amount != 2 || expressions.query.setExprs[1].operator != "-" {
		t.Fatalf("Inc/Dec 代理状态错误: %#v", expressions.query.setExprs)
	}
}

// TestModelConfigurationAndConvenienceMethods 验证自动表名、配置快照及 Model 便捷方法，
// 同时确保非法配置通过终端方法返回而不是被后续合法配置覆盖。
func TestModelConfigurationAndConvenienceMethods(t *testing.T) {
	database := NewDB(&mockConnection{})
	model, err := NewModelAuto(database, modelHardeningUser{})
	if err != nil {
		t.Fatalf("自动创建模型失败: %v", err)
	}
	if err := model.SetDB(database); err != nil {
		t.Fatalf("设置模型数据库失败: %v", err)
	}
	model.Table("accounts").AutoTimestamp(true).CreateTimeField("created_at").UpdateTimeField("updated_at").PrimaryKey("account_id")
	model.mu.RLock()
	configuration := []interface{}{model.table, model.autoTimestamp, model.createTimeField, model.updateTimeField, model.primaryKey}
	model.mu.RUnlock()
	want := []interface{}{"accounts", true, "created_at", "updated_at", "account_id"}
	if !reflect.DeepEqual(configuration, want) {
		t.Fatalf("模型配置错误: got=%#v want=%#v", configuration, want)
	}

	if query := model.Order("account_id DESC").Limit(5).Field("account_id").Page(2, 5); query.query.order != "account_id DESC" || query.query.limit != 5 || query.query.offset != 5 || query.query.fields != "account_id" {
		t.Fatalf("Model 便捷查询代理错误: %#v", query.query)
	}
	if query := model.Limit(3); query.query.limit != 3 {
		t.Fatalf("Model.Limit 未保留限制条件: %#v", query.query)
	}
	if query := model.Field("account_id,name"); query.query.fields != "account_id,name" {
		t.Fatalf("Model.Field 未保留字段条件: %#v", query.query)
	}
	if query := model.Page(3, 4); query.query.limit != 4 || query.query.offset != 8 {
		t.Fatalf("Model.Page 未保留分页条件: %#v", query.query)
	}
	if _, err := model.Count(); err != nil {
		t.Fatalf("Model.Count 失败: %v", err)
	}
	if _, err := model.UpdateMap(map[string]interface{}{"name": "unsafe"}); !errors.Is(err, ErrUnsafeFullTableMutation) {
		t.Fatalf("Model.UpdateMap 无条件更新应被拦截，实际为 %v", err)
	}
	if _, err := model.DeleteRecord(); !errors.Is(err, ErrUnsafeFullTableMutation) {
		t.Fatalf("Model.DeleteRecord 无条件删除应被拦截，实际为 %v", err)
	}
	if err := model.Delete(); !errors.Is(err, ErrUnsafeFullTableMutation) {
		t.Fatalf("Model.Delete 无条件删除应被拦截，实际为 %v", err)
	}
	if err := model.ForceDelete(); !errors.Is(err, ErrUnsafeFullTableMutation) {
		t.Fatalf("Model.ForceDelete 无条件物理删除应被拦截，实际为 %v", err)
	}
	if err := model.SoftDelete("deleted_at").Restore(); !errors.Is(err, ErrUnsafeFullTableMutation) {
		t.Fatalf("Model.Restore 无条件恢复应被拦截，实际为 %v", err)
	}

	invalid := NewModel(database, "users").Table("bad table").CreateTimeField("bad field")
	if _, err := invalid.Select(); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("非法模型配置应返回 ErrInvalidModel，实际为 %v", err)
	}
	if err := (*Model)(nil).SetDB(database); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("nil 模型 SetDB 应返回 ErrInvalidModel，实际为 %v", err)
	}
}

// TestModelQueryTerminalBusinessFlows 验证模型终端方法真实执行修改器、获取器、
// 分页、分块、聚合和高级查询，并保持调用方输入不可变。
func TestModelQueryTerminalBusinessFlows(t *testing.T) {
	connection := &modelBusinessConnection{
		rows: []map[string]interface{}{
			{"id": int64(1), "name": "alice", "score": 10},
			{"id": int64(2), "name": "bob", "score": 15},
		},
		count:       2,
		aggregate:   "12.5",
		insertID:    42,
		updateCount: 2,
		deleteCount: 1,
	}
	model := NewModel(NewDB(connection), "users")
	if err := model.Getter("name", func(value interface{}, _ map[string]interface{}) interface{} {
		return strings.ToUpper(value.(string))
	}); err != nil {
		t.Fatalf("注册获取器失败: %v", err)
	}
	if err := model.Setter("name", func(value interface{}, _ map[string]interface{}) interface{} {
		return strings.ToLower(value.(string))
	}); err != nil {
		t.Fatalf("注册修改器失败: %v", err)
	}

	row, err := model.Find()
	if err != nil || row["name"] != "ALICE" {
		t.Fatalf("Model.Find 获取器结果错误: row=%#v err=%v", row, err)
	}
	rows, err := model.Select()
	if err != nil || len(rows) != 2 || rows[1]["name"] != "BOB" {
		t.Fatalf("Model.Select 获取器结果错误: rows=%#v err=%v", rows, err)
	}
	if count, err := model.Count(); err != nil || count != 2 {
		t.Fatalf("Model.Count 结果错误: count=%d err=%v", count, err)
	}

	insertData := map[string]interface{}{"name": "ADA"}
	if id, err := model.InsertGetId(insertData); err != nil || id != int64(42) {
		t.Fatalf("Model.InsertGetId 结果错误: id=%v err=%v", id, err)
	}
	if insertData["name"] != "ADA" || connection.insertData["name"] != "ada" {
		t.Fatalf("修改器必须只修改内部副本: caller=%#v driver=%#v", insertData, connection.insertData)
	}
	updateData := map[string]interface{}{"name": "GRACE"}
	if affected, err := model.newModelQuery().WhereField("id", "=", 1).Update(updateData); err != nil || affected != 2 {
		t.Fatalf("ModelQuery.Update 结果错误: affected=%d err=%v", affected, err)
	}
	if updateData["name"] != "GRACE" || connection.updateData["name"] != "grace" {
		t.Fatalf("更新修改器必须只修改内部副本: caller=%#v driver=%#v", updateData, connection.updateData)
	}
	if affected, err := model.newModelQuery().WhereField("id", "=", 1).Delete(); err != nil || affected != 1 {
		t.Fatalf("ModelQuery.Delete 结果错误: affected=%d err=%v", affected, err)
	}
	if affected, err := model.newModelQuery().WhereField("id", "=", 1).ForceDelete(); err != nil || affected != 1 {
		t.Fatalf("ModelQuery.ForceDelete 结果错误: affected=%d err=%v", affected, err)
	}

	softModel := NewModel(model.db, "users").SoftDelete("deleted_at")
	if affected, err := softModel.newModelQuery().WhereField("id", "=", 1).Restore(); err != nil || affected != 2 {
		t.Fatalf("ModelQuery.Restore 结果错误: affected=%d err=%v", affected, err)
	}
	if _, err := NewModel(model.db, "users").newModelQuery().WhereField("id", "=", 1).Restore(); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("非软删除模型 Restore 应返回 ErrInvalidModel，实际为 %v", err)
	}
	if _, err := NewModel(model.db, "users").OnlyTrashed().Select(); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("非软删除模型 OnlyTrashed 应返回 ErrInvalidModel，实际为 %v", err)
	}
	if where := softModel.OnlyTrashed().PrepareQuery().where; len(where) != 1 || !strings.Contains(where[0], "IS NOT NULL") {
		t.Fatalf("OnlyTrashed 条件错误: %v", where)
	}
	if where := softModel.WithTrashed().PrepareQuery().where; len(where) != 0 {
		t.Fatalf("WithTrashed 不应注入软删除条件: %v", where)
	}

	paginator, err := model.newModelQuery().Paginate(2, 1)
	if err != nil || paginator.Total != 2 || len(paginator.List) != 1 || paginator.List[0]["name"] != "BOB" {
		t.Fatalf("模型分页结果错误: paginator=%#v err=%v", paginator, err)
	}
	chunkCalls := 0
	if err := model.newModelQuery().Chunk(1, func(rows []map[string]interface{}) bool {
		chunkCalls++
		return false
	}); err != nil || chunkCalls != 1 {
		t.Fatalf("模型 Chunk 结果错误: calls=%d err=%v", chunkCalls, err)
	}
	cursorCalls := 0
	if err := model.newModelQuery().ChunkById(10, "id", func(rows []map[string]interface{}) bool {
		cursorCalls++
		return false
	}); err != nil || cursorCalls != 1 {
		t.Fatalf("模型 ChunkById 结果错误: calls=%d err=%v", cursorCalls, err)
	}

	for name, aggregate := range map[string]func(string) (float64, error){
		"sum": model.newModelQuery().Sum,
		"avg": model.newModelQuery().Avg,
		"max": model.newModelQuery().Max,
		"min": model.newModelQuery().Min,
	} {
		value, err := aggregate("score")
		if err != nil || value != 12.5 {
			t.Fatalf("模型 %s 聚合结果错误: value=%v err=%v", name, value, err)
		}
	}
	if value, err := model.newModelQuery().Value("name"); err != nil || value != "ALICE" {
		t.Fatalf("模型 Value 获取器结果错误: value=%#v err=%v", value, err)
	}
	column, err := model.newModelQuery().Column("name")
	if err != nil || !reflect.DeepEqual(column, []interface{}{"ALICE", "BOB"}) {
		t.Fatalf("模型 Column 列表错误: value=%#v err=%v", column, err)
	}
	keyed, err := model.newModelQuery().Column("name", "id")
	if err != nil || !reflect.DeepEqual(keyed, map[string]interface{}{"1": "ALICE", "2": "BOB"}) {
		t.Fatalf("模型 Column 映射错误: value=%#v err=%v", keyed, err)
	}

	bulkInput := []map[string]interface{}{{"name": "BULK"}}
	if _, err := model.newModelQuery().InsertAll(bulkInput); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("非 SQL 模型批量插入应返回 ErrInvalidQuery，实际为 %v", err)
	}
	if bulkInput[0]["name"] != "BULK" {
		t.Fatalf("批量插入不得修改调用方数据: %#v", bulkInput)
	}

	ctx := context.WithValue(context.Background(), struct{ name string }{"trace"}, "ctx-1")
	advancedRows, err := model.newModelQuery().WithContext(ctx).Join("profiles", "users.id = profiles.user_id").Select()
	if err != nil || len(advancedRows) != 2 || connection.lastContext != ctx || !strings.Contains(connection.lastRawSQL, "JOIN profiles") {
		t.Fatalf("模型高级查询或上下文传播错误: rows=%#v sql=%q err=%v", advancedRows, connection.lastRawSQL, err)
	}
	if affected, err := model.newModelQuery().WithContext(ctx).WhereField("id", "=", 1).Inc("score", 2).Update(nil); err != nil || affected != 2 || connection.lastContext != ctx {
		t.Fatalf("模型自增执行错误: affected=%d sql=%q args=%v err=%v", affected, connection.lastRawSQL, connection.lastRawArgs, err)
	}
}

// TestModelSeekPageAppliesGetters 验证模型游标分页继承主键默认值并应用获取器。
func TestModelSeekPageAppliesGetters(t *testing.T) {
	connection := &chunkRecorderConn{pages: [][]map[string]interface{}{
		{{"id": int64(1), "name": "alice"}, {"id": int64(2), "name": "bob"}},
		{{"id": int64(2), "name": "bob"}},
	}}
	model := NewModel(NewDB(connection), "users")
	if err := model.Getter("name", func(value interface{}, _ map[string]interface{}) interface{} {
		return strings.ToUpper(value.(string))
	}); err != nil {
		t.Fatalf("注册模型获取器失败: %v", err)
	}

	first, err := model.newModelQuery().SeekPage(1, "", nil)
	if err != nil {
		t.Fatalf("模型首个游标分页失败: %v", err)
	}
	if len(first.List) != 1 || first.List[0]["name"] != "ALICE" || first.NextCursor != int64(1) || !first.HasMore {
		t.Fatalf("模型首个游标分页结果错误: %#v", first)
	}
	second, err := model.newModelQuery().SeekPage(1, "", first.NextCursor)
	if err != nil {
		t.Fatalf("模型后续游标分页失败: %v", err)
	}
	if len(second.List) != 1 || second.List[0]["name"] != "BOB" || second.NextCursor != nil || second.HasMore {
		t.Fatalf("模型末页游标分页结果错误: %#v", second)
	}
	if connection.countCalls != 0 {
		t.Fatalf("模型 SeekPage 不应执行 COUNT，实际执行 %d 次", connection.countCalls)
	}
}

// TestModelImmediateRelations 验证一对一、一对多、反向和多对多立即关联都复用安全查询入口。
func TestModelImmediateRelations(t *testing.T) {
	connection := &modelBusinessConnection{
		tableRows: map[string][]map[string]interface{}{
			"profiles":  {{"id": int64(1), "user_id": int64(7), "label": "main"}},
			"user_role": {{"user_id": int64(7), "role_id": int64(10)}},
			"roles":     {{"id": int64(10), "name": "admin"}},
		},
	}
	database := NewDB(connection)
	users := NewModel(database, "users")
	profiles := NewModel(database, "profiles")
	roles := NewModel(database, "roles")

	if row, err := users.HasOne(profiles, "user_id", int64(7)); err != nil || row["label"] != "main" {
		t.Fatalf("HasOne 结果错误: row=%#v err=%v", row, err)
	}
	if rows, err := users.HasMany(profiles, "user_id", int64(7)); err != nil || len(rows) != 1 {
		t.Fatalf("HasMany 结果错误: rows=%#v err=%v", rows, err)
	}
	if row, err := users.BelongsTo(profiles, int64(1), "id"); err != nil || row["label"] != "main" {
		t.Fatalf("BelongsTo 结果错误: row=%#v err=%v", row, err)
	}
	if rows, err := users.BelongsToMany(roles, "user_role", "user_id", "role_id", int64(7)); err != nil || len(rows) != 1 || rows[0]["name"] != "admin" {
		t.Fatalf("BelongsToMany 结果错误: rows=%#v err=%v", rows, err)
	}
	if _, err := users.HasOne(nil, "user_id", int64(7)); !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("nil 立即关联应返回 ErrInvalidRelation，实际为 %v", err)
	}
}

// TestModelRelationPreloadingCoversAllRelationTypes 验证批量预加载按强类型键组装
// 一对一、反向和多对多关系，空外键保持空关系而不会串到数值零。
func TestModelRelationPreloadingCoversAllRelationTypes(t *testing.T) {
	connection := &modelBusinessConnection{
		tableRows: map[string][]map[string]interface{}{
			"users": {
				{"id": int64(1), "manager_id": int64(10), "name": "alice"},
				{"id": int64(2), "manager_id": nil, "name": "bob"},
			},
			"profiles": {
				{"id": int64(20), "user_id": int64(1), "label": "primary"},
			},
			"managers": {
				{"id": int64(10), "name": "manager"},
			},
			"user_role": {
				{"user_id": int64(1), "role_id": int64(100)},
				{"user_id": int64(1), "role_id": int64(101)},
				{"user_id": int64(2), "role_id": int64(101)},
			},
			"roles": {
				{"id": int64(100), "name": "admin"},
				{"id": int64(101), "name": "viewer"},
			},
		},
	}
	database := NewDB(connection)
	users := NewModel(database, "users")
	profiles := NewModel(database, "profiles")
	managers := NewModel(database, "managers")
	roles := NewModel(database, "roles")
	if err := users.DefineHasOne("profile", profiles, "user_id", "id"); err != nil {
		t.Fatalf("定义 HasOne 失败: %v", err)
	}
	if err := users.DefineBelongsTo("manager", managers, "manager_id", "id"); err != nil {
		t.Fatalf("定义 BelongsTo 失败: %v", err)
	}
	if err := users.DefineBelongsToMany("roles", roles, "user_role", "user_id", "role_id", "id"); err != nil {
		t.Fatalf("定义 BelongsToMany 失败: %v", err)
	}

	rows, err := users.With("profile", "manager", "roles").Select()
	if err != nil {
		t.Fatalf("预加载全部关联失败: %v", err)
	}
	if rows[0]["profile"].(map[string]interface{})["label"] != "primary" || rows[0]["manager"].(map[string]interface{})["name"] != "manager" {
		t.Fatalf("第一行单值关联错误: %#v", rows[0])
	}
	if profile, ok := rows[1]["profile"].(map[string]interface{}); !ok || profile != nil {
		t.Fatalf("无匹配 HasOne 应返回类型化 nil map，实际为 %#v", rows[1]["profile"])
	}
	if manager, ok := rows[1]["manager"].(map[string]interface{}); !ok || manager != nil {
		t.Fatalf("空外键 BelongsTo 应返回类型化 nil map，实际为 %#v", rows[1]["manager"])
	}
	if firstRoles := rows[0]["roles"].([]map[string]interface{}); len(firstRoles) != 2 {
		t.Fatalf("第一行多对多关联数量错误: %#v", firstRoles)
	}
	if secondRoles := rows[1]["roles"].([]map[string]interface{}); len(secondRoles) != 1 || secondRoles[0]["name"] != "viewer" {
		t.Fatalf("第二行多对多关联错误: %#v", secondRoles)
	}

	if _, err := users.With("profile", "profile").Select(); !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("重复预加载应返回 ErrInvalidRelation，实际为 %v", err)
	}
	if _, err := users.With("undefined").Select(); !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("未定义关联应返回 ErrInvalidRelation，实际为 %v", err)
	}
}
