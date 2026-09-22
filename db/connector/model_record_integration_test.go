package connector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

const modelRecordOperationTimeout = 3 * time.Second

type sqliteRecordUser struct {
	*db.Model
	ID     int64  `thinkgo:"id"`
	Name   string `thinkgo:"name"`
	Status int64  `thinkgo:"status,omitempty"`
}

type sqliteBusinessKeyRecord struct {
	*db.Model
	Code string `thinkgo:"code"`
	Name string `thinkgo:"name"`
}

// newSQLiteRecordModel 为每个测试绑定独立模型，沿用现有单连接内存数据库夹具。
func newSQLiteRecordModel(t *testing.T, ctx context.Context, database *db.DB, user *sqliteRecordUser) *db.Model {
	t.Helper()
	model, err := db.NewModelFor(ctx, database, user)
	if err != nil {
		t.Fatalf("绑定 SQLite 模型失败: %v", err)
	}
	return model.Table("users").AutoTimestamp(false)
}

// TestSQLiteRecordTypedFindSelectAndSave 验证查询出的结构体已具备记录身份，并可独立保存和删除。
func TestSQLiteRecordTypedFindSelectAndSave(t *testing.T) {
	database := newSqliteTestDB(t)
	t.Cleanup(func() { _ = database.Close() })
	created := &sqliteRecordUser{Name: "Ada", Status: 1}
	model := newSQLiteRecordModel(t, context.Background(), database, created)
	if created.Model != model {
		t.Fatal("模型绑定必须注入嵌入的 Model 字段")
	}
	if err := created.Save(); err != nil || created.ID == 0 {
		t.Fatalf("新模型保存失败: id=%d err=%v", created.ID, err)
	}
	var found sqliteRecordUser
	exists, err := model.Where("id", created.ID).Find(&found)
	if err != nil || !exists || found.Model == nil || found.Name != "Ada" {
		t.Fatalf("类型化查询或记录注入失败: found=%#v exists=%v err=%v", found, exists, err)
	}
	found.Name = "Grace"
	if err := found.Save(); err != nil {
		t.Fatalf("保存查询出的模型失败: %v", err)
	}
	var records []*sqliteRecordUser
	if err := model.Order("id").Select(&records); err != nil || len(records) != 1 || records[0].Name != "Grace" {
		t.Fatalf("类型化列表失败: records=%#v err=%v", records, err)
	}
	records[0].Status = 0
	if err := records[0].Save(); err != nil {
		t.Fatalf("列表元素独立保存失败: %v", err)
	}
	row, err := database.Table("users").Where("id", created.ID).Find()
	if err != nil || row["status"] != int64(0) {
		t.Fatalf("omitempty 字段的显式零值未持久化: row=%#v err=%v", row, err)
	}
	if err := records[0].Delete(); err != nil {
		t.Fatalf("按记录身份删除失败: %v", err)
	}
	var missing sqliteRecordUser
	if exists, err := model.Where("id", created.ID).Find(&missing); err != nil || exists {
		t.Fatalf("删除后的记录不应命中: exists=%v err=%v", exists, err)
	}
}

// TestSQLiteRecordSavePreservesUnloadedColumns 验证字段裁剪不会将未加载的零值覆盖到数据库。
func TestSQLiteRecordSavePreservesUnloadedColumns(t *testing.T) {
	database := newSqliteTestDB(t)
	t.Cleanup(func() { _ = database.Close() })
	owner := &sqliteRecordUser{Name: "Ada", Status: 7}
	model := newSQLiteRecordModel(t, context.Background(), database, owner)
	if err := owner.Save(); err != nil {
		t.Fatal(err)
	}
	var partial sqliteRecordUser
	if exists, err := model.Field("id,name").Where("id", owner.ID).Find(&partial); err != nil || !exists {
		t.Fatalf("部分字段查询失败: exists=%v err=%v", exists, err)
	}
	partial.Name = ""
	if err := partial.Save(); err != nil {
		t.Fatalf("保存显式空字符串失败: %v", err)
	}
	row, err := database.Table("users").Where("id", owner.ID).Find()
	if err != nil || row["name"] != "" || row["status"] != int64(7) {
		t.Fatalf("部分字段保存覆盖了未加载字段: row=%#v err=%v", row, err)
	}
	var unidentified sqliteRecordUser
	if exists, err := model.Field("name").Where("id", owner.ID).Find(&unidentified); err != nil || !exists {
		t.Fatalf("无主键字段查询失败: exists=%v err=%v", exists, err)
	}
	unidentified.Name = "不可保存"
	if err := unidentified.Save(); err == nil {
		t.Fatal("缺失记录身份时必须拒绝保存")
	}
}

// TestSQLiteRecordRejectsChangedPrimaryKey 验证更改或清空已加载主键不会更新另一行或插入新记录。
func TestSQLiteRecordRejectsChangedPrimaryKey(t *testing.T) {
	for _, testCase := range []struct {
		name string
		key  int64
	}{
		{name: "清空主键", key: 0},
		{name: "改为其他主键", key: 99},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			database := newSqliteTestDB(t)
			t.Cleanup(func() { _ = database.Close() })
			owner := &sqliteRecordUser{Name: "Ada", Status: 1}
			model := newSQLiteRecordModel(t, context.Background(), database, owner)
			if err := owner.Save(); err != nil {
				t.Fatal(err)
			}
			originalKey := owner.ID
			var loaded sqliteRecordUser
			if exists, err := model.Where("id", originalKey).Find(&loaded); err != nil || !exists {
				t.Fatalf("加载记录失败: exists=%v err=%v", exists, err)
			}
			loaded.ID = testCase.key
			loaded.Name = "错误目标"
			if err := loaded.Save(); err == nil {
				t.Fatal("已加载主键变化必须拒绝保存")
			}
			if err := loaded.Delete(); err == nil {
				t.Fatal("已加载主键变化必须拒绝删除")
			}
			row, err := database.Table("users").Where("id", originalKey).Find()
			count, countErr := database.Table("users").Count()
			if err != nil || countErr != nil || count != 1 || row["name"] != "Ada" {
				t.Fatalf("身份变化导致错误写入: row=%#v count=%d err=%v countErr=%v", row, count, err, countErr)
			}
		})
	}
}

// TestSQLiteRecordNewBusinessPrimaryKeyCreates 验证新对象已指定业务主键时仍执行插入。
func TestSQLiteRecordNewBusinessPrimaryKeyCreates(t *testing.T) {
	database := newSqliteTestDB(t)
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Execute("CREATE TABLE record_keys (code TEXT PRIMARY KEY, name TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	owner := &sqliteBusinessKeyRecord{Code: "user-ada", Name: "Ada"}
	model, err := db.NewModelFor(context.Background(), database, owner)
	if err != nil {
		t.Fatal(err)
	}
	model.Table("record_keys").PrimaryKey("code").AutoTimestamp(false)
	if err := owner.Save(); err != nil {
		t.Fatalf("带业务主键的新记录应插入: %v", err)
	}
	owner.Name = "Grace"
	if err := owner.Save(); err != nil {
		t.Fatalf("插入后的记录应更新: %v", err)
	}
	row, err := database.Table("record_keys").Where("code", owner.Code).Find()
	count, countErr := database.Table("record_keys").Count()
	if err != nil || countErr != nil || count != 1 || row["name"] != "Grace" {
		t.Fatalf("业务主键保存分支错误: row=%#v count=%d err=%v countErr=%v", row, count, err, countErr)
	}
}

// TestSQLiteRecordTransactionRollback 验证类型化模型和查询结果始终留在事务内，回滚后无写入残留。
func TestSQLiteRecordTransactionRollback(t *testing.T) {
	database := newSqliteTestDB(t)
	t.Cleanup(func() { _ = database.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), modelRecordOperationTimeout)
	defer cancel()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transaction.Rollback() })
	owner := &sqliteRecordUser{Name: "事务内", Status: 1}
	model, err := db.NewModelFor(ctx, database, owner, db.WithModelTransaction(transaction))
	if err != nil {
		t.Fatal(err)
	}
	model.Table("users").AutoTimestamp(false)
	if err := owner.Save(); err != nil {
		t.Fatalf("事务模型插入失败: %v", err)
	}
	var loaded sqliteRecordUser
	if exists, err := model.Where("id", owner.ID).Find(&loaded); err != nil || !exists {
		t.Fatalf("事务内未提交记录必须可见: exists=%v err=%v", exists, err)
	}
	loaded.Name = "已修改"
	if err := loaded.Save(); err != nil {
		t.Fatalf("事务内查询结果保存失败: %v", err)
	}
	var records []*sqliteRecordUser
	if err := model.Select(&records); err != nil || len(records) != 1 || records[0].Name != "已修改" {
		t.Fatalf("事务内指针列表查询失败: records=%#v err=%v", records, err)
	}
	if err := records[0].Delete(); err != nil {
		t.Fatalf("事务内记录删除失败: %v", err)
	}
	if count, err := model.Count(); err != nil || count != 0 {
		t.Fatalf("事务内删除未生效: count=%d err=%v", count, err)
	}
	remaining := &sqliteRecordUser{Name: "等待回滚", Status: 1}
	if err := model.Create(remaining); err != nil {
		t.Fatalf("事务内显式创建失败: %v", err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if count, err := database.Table("users").Count(); err != nil || count != 0 {
		t.Fatalf("回滚后模型写入仍残留: count=%d err=%v", count, err)
	}
	loaded.Name = "不可逃逸"
	if err := loaded.Save(); !errors.Is(err, db.ErrTransactionDone) {
		t.Fatalf("事务结束后的模型不能退回普通连接: %v", err)
	}
	if count, err := database.Table("users").Count(); err != nil || count != 0 {
		t.Fatalf("已结束事务的模型发生连接逃逸: count=%d err=%v", count, err)
	}
}

// TestSQLiteRecordTransactionRelations 验证关联目标与中间表读取同一事务的未提交数据。
func TestSQLiteRecordTransactionRelations(t *testing.T) {
	database := newSqliteTestDB(t)
	t.Cleanup(func() { _ = database.Close() })
	for _, statement := range []string{
		"CREATE TABLE profiles (id INTEGER PRIMARY KEY, user_id INTEGER, bio TEXT)",
		"CREATE TABLE roles (id INTEGER PRIMARY KEY, name TEXT)",
		"CREATE TABLE user_roles (user_id INTEGER, role_id INTEGER)",
	} {
		if _, err := database.Execute(statement); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), modelRecordOperationTimeout)
	defer cancel()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transaction.Rollback() })
	for _, statement := range []string{
		"INSERT INTO users(id,name,status) VALUES(1,'Ada',1)",
		"INSERT INTO profiles(id,user_id,bio) VALUES(2,1,'profile')",
		"INSERT INTO orders(id,user_id,amount) VALUES(3,1,10)",
		"INSERT INTO roles(id,name) VALUES(4,'admin')",
		"INSERT INTO user_roles(user_id,role_id) VALUES(1,4)",
	} {
		if _, err := transaction.ExecuteContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	users := db.NewModel(database, "users").WithContext(ctx).WithTx(transaction)
	profiles := db.NewModel(database, "profiles")
	orders := db.NewModel(database, "orders")
	roles := db.NewModel(database, "roles")
	if err := users.DefineHasOne("profile", profiles, "user_id", "id"); err != nil {
		t.Fatal(err)
	}
	if err := users.DefineHasMany("orders", orders, "user_id", "id"); err != nil {
		t.Fatal(err)
	}
	if err := users.DefineBelongsToMany("roles", roles, "user_roles", "user_id", "role_id", "id"); err != nil {
		t.Fatal(err)
	}
	row, err := users.With("profile", "orders", "roles").Where("id", 1).FindMap()
	if err != nil {
		t.Fatalf("事务关联预加载失败: %v", err)
	}
	profile, profileOK := row["profile"].(map[string]interface{})
	loadedOrders, ordersOK := row["orders"].([]map[string]interface{})
	loadedRoles, rolesOK := row["roles"].([]map[string]interface{})
	if !profileOK || profile["bio"] != "profile" || !ordersOK || len(loadedOrders) != 1 || !rolesOK || len(loadedRoles) != 1 {
		t.Fatalf("事务未提交关联不可见: %#v", row)
	}
	if profile, err := users.HasOne(profiles, "user_id", int64(1)); err != nil || profile["bio"] != "profile" {
		t.Fatalf("立即一对一关联未绑定事务: profile=%#v err=%v", profile, err)
	}
	if orders, err := users.HasMany(orders, "user_id", int64(1)); err != nil || len(orders) != 1 {
		t.Fatalf("立即一对多关联未绑定事务: orders=%#v err=%v", orders, err)
	}
	if roles, err := users.BelongsToMany(roles, "user_roles", "user_id", "role_id", int64(1)); err != nil || len(roles) != 1 {
		t.Fatalf("立即多对多关联未绑定事务: roles=%#v err=%v", roles, err)
	}
	transactionOrders := orders.WithContext(ctx).WithTx(transaction)
	if err := transactionOrders.DefineBelongsTo("user", db.NewModel(database, "users"), "user_id", "id"); err != nil {
		t.Fatal(err)
	}
	order, err := transactionOrders.With("user").FindMap()
	if err != nil {
		t.Fatalf("反向关联预加载失败: %v", err)
	}
	if user, ok := order["user"].(map[string]interface{}); !ok || user["name"] != "Ada" {
		t.Fatalf("反向关联记录错误: %#v", order)
	}
	if user, err := transactionOrders.BelongsTo(db.NewModel(database, "users"), int64(1), "id"); err != nil || user["name"] != "Ada" {
		t.Fatalf("立即反向关联未绑定事务: user=%#v err=%v", user, err)
	}
	foreignDatabase := newSqliteTestDB(t)
	t.Cleanup(func() { _ = foreignDatabase.Close() })
	if _, err := users.HasMany(db.NewModel(foreignDatabase, "orders"), "user_id", int64(1)); !errors.Is(err, db.ErrInvalidRelation) {
		t.Fatalf("事务关联跨数据库必须拒绝: %v", err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if count, err := database.Table("users").Count(); err != nil || count != 0 {
		t.Fatalf("关联预加载不应影响回滚: count=%d err=%v", count, err)
	}
}

// TestSQLiteRecordCancellationDoesNotWrite 验证记录绑定取消后即使重新派生查询也不会产生写入。
func TestSQLiteRecordCancellationDoesNotWrite(t *testing.T) {
	database := newSqliteTestDB(t)
	t.Cleanup(func() { _ = database.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner := &sqliteRecordUser{Name: "Ada", Status: 1}
	model := newSQLiteRecordModel(t, ctx, database, owner)
	if err := owner.Save(); err != nil {
		t.Fatal(err)
	}
	cancel()
	owner.Name = "已取消的更新"
	if err := owner.Save(); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后的记录保存必须返回取消错误: %v", err)
	}
	if err := owner.Delete(); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后的记录删除必须返回取消错误: %v", err)
	}
	var records []*sqliteRecordUser
	if err := model.Where("id", owner.ID).Select(&records); !errors.Is(err, context.Canceled) {
		t.Fatalf("派生查询必须继承取消信号: %v", err)
	}
	row, err := database.Table("users").Where("id", owner.ID).Find()
	if err != nil || row["name"] != "Ada" {
		t.Fatalf("取消后的操作改变了数据库: row=%#v err=%v", row, err)
	}
}
