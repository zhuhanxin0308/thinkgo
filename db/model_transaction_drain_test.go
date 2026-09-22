package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

const modelTransactionDrainTimeout = 3 * time.Second

type modelTransactionDrainRecord struct {
	*Model
	ID   int64  `thinkgo:"id"`
	Name string `thinkgo:"name"`
}

// TestModelTransactionSQLiteDuringClose 验证关闭期间已有事务的模型与关联使用原连接完成工作。
func TestModelTransactionSQLiteDuringClose(t *testing.T) {
	for _, action := range []string{"commit", "rollback"} {
		t.Run(action, func(t *testing.T) {
			database, raw := newModelScanSQLite(t)
			for _, statement := range []string{
				"CREATE TABLE drain_users (id INTEGER PRIMARY KEY, name TEXT NOT NULL)",
				"CREATE TABLE drain_profiles (id INTEGER PRIMARY KEY, user_id INTEGER, bio TEXT)",
				"CREATE TABLE drain_orders (id INTEGER PRIMARY KEY, user_id INTEGER, amount INTEGER)",
				"CREATE TABLE drain_roles (id INTEGER PRIMARY KEY, name TEXT)",
				"CREATE TABLE drain_user_roles (user_id INTEGER, role_id INTEGER)",
			} {
				if _, err := raw.Exec(statement); err != nil {
					t.Fatalf("创建事务关闭测试表失败: %v", err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), modelTransactionDrainTimeout)
			defer cancel()
			transaction, err := database.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = transaction.Rollback() })
			for _, statement := range []string{
				"INSERT INTO drain_users(id,name) VALUES(1,'Ada')",
				"INSERT INTO drain_profiles(id,user_id,bio) VALUES(2,1,'profile')",
				"INSERT INTO drain_orders(id,user_id,amount) VALUES(3,1,10)",
				"INSERT INTO drain_roles(id,name) VALUES(4,'admin')",
				"INSERT INTO drain_user_roles(user_id,role_id) VALUES(1,4)",
			} {
				if _, err := transaction.ExecuteContext(ctx, statement); err != nil {
					t.Fatalf("事务内准备未提交关联失败: %v", err)
				}
			}

			// 关联模板保持普通模型，查询时必须从主模型继承事务和上下文。
			users := NewModel(database, "drain_users").WithContext(ctx).WithTx(transaction)
			profiles := NewModel(database, "drain_profiles")
			orders := NewModel(database, "drain_orders")
			roles := NewModel(database, "drain_roles")
			if err := users.DefineHasOne("profile", profiles, "user_id", "id"); err != nil {
				t.Fatal(err)
			}
			if err := users.DefineHasMany("orders", orders, "user_id", "id"); err != nil {
				t.Fatal(err)
			}
			if err := users.DefineBelongsToMany("roles", roles, "drain_user_roles", "user_id", "role_id", "id"); err != nil {
				t.Fatal(err)
			}
			transactionOrders := orders.WithContext(ctx).WithTx(transaction)
			if err := transactionOrders.DefineBelongsTo("user", NewModel(database, "drain_users"), "user_id", "id"); err != nil {
				t.Fatal(err)
			}
			ordinaryQuery := database.Table("drain_users").WithContext(ctx)

			closeDone := make(chan error, 1)
			go func() { closeDone <- database.Close() }()
			waitForDatabaseCloseLock(t, database)
			select {
			case err := <-closeDone:
				t.Fatalf("事务完成前数据库关闭不应返回: %v", err)
			default:
			}
			if _, err := database.Table("drain_users").WithContext(ctx).Select(); !errors.Is(err, ErrDatabaseClosed) {
				t.Fatalf("关闭期间必须拒绝普通新查询: %v", err)
			}
			if _, err := ordinaryQuery.Select(); !errors.Is(err, ErrDatabaseClosed) {
				t.Fatalf("关闭前构造的普通查询也不能借用活动事务: %v", err)
			}
			if _, err := database.ExecuteContext(ctx, "INSERT INTO drain_users(name) VALUES(?)", "不可写入"); !errors.Is(err, ErrDatabaseClosed) {
				t.Fatalf("关闭期间必须拒绝普通写入: %v", err)
			}
			if next, err := database.BeginTx(ctx, nil); !errors.Is(err, ErrDatabaseClosed) {
				if next != nil {
					_ = next.Rollback()
				}
				t.Fatalf("关闭期间必须拒绝新事务: %v", err)
			}

			// 关闭后仍允许向既有事务绑定新记录，包括主键解析和查询后的脏字段保存。
			owner := &modelTransactionDrainRecord{Name: "关闭期间创建"}
			model, err := NewModelFor(ctx, database, owner, WithModelTransaction(transaction))
			if err != nil {
				t.Fatalf("关闭期间向已有事务绑定模型失败: %v", err)
			}
			model.Table("drain_users").AutoTimestamp(false)
			if err := model.Create(owner); err != nil || owner.ID == 0 {
				t.Fatalf("关闭期间事务模型创建失败: id=%d err=%v", owner.ID, err)
			}
			var loaded modelTransactionDrainRecord
			if found, err := model.Where("id", owner.ID).Find(&loaded); err != nil || !found {
				t.Fatalf("关闭期间事务类型化查询失败: found=%v err=%v", found, err)
			}
			loaded.Name = "关闭期间已修改"
			if err := loaded.Save(); err != nil {
				t.Fatalf("关闭期间事务模型保存失败: %v", err)
			}
			row, err := transaction.Table("drain_users").Where("id", owner.ID).Find()
			if err != nil || row["name"] != loaded.Name {
				t.Fatalf("事务保存未在原事务生效: row=%#v err=%v", row, err)
			}

			assertModelTransactionDrainRelations(t, users, profiles, orders, roles, transactionOrders)
			if err := loaded.Delete(); err != nil {
				t.Fatalf("关闭期间事务模型删除失败: %v", err)
			}
			if count, err := users.Where("id", owner.ID).Count(); err != nil || count != 0 {
				t.Fatalf("事务删除未在原事务生效: count=%d err=%v", count, err)
			}
			select {
			case err := <-closeDone:
				t.Fatalf("事务仍活动时数据库关闭提前返回: %v", err)
			default:
			}
			if action == "commit" {
				err = transaction.Commit()
			} else {
				err = transaction.Rollback()
			}
			if err != nil {
				t.Fatalf("关闭期间结束事务失败: %v", err)
			}
			select {
			case err := <-closeDone:
				if err != nil {
					t.Fatalf("事务完成后数据库关闭失败: %v", err)
				}
			case <-time.After(modelTransactionDrainTimeout):
				t.Fatal("事务完成后数据库关闭仍未返回")
			}
			if _, err := users.FindMap(); !errors.Is(err, ErrTransactionDone) {
				t.Fatalf("已结束事务模型不能回退到已关闭普通连接: %v", err)
			}
		})
	}
}

// assertModelTransactionDrainRelations 同时核验立即关联和预加载，覆盖中间表查询的事务传递。
func assertModelTransactionDrainRelations(t *testing.T, users, profiles, orders, roles, transactionOrders *Model) {
	t.Helper()
	row, err := users.With("profile", "orders", "roles").Where("id", 1).FindMap()
	if err != nil {
		t.Fatalf("关闭期间事务关联预加载失败: %v", err)
	}
	profile, profileOK := row["profile"].(map[string]any)
	loadedOrders, ordersOK := row["orders"].([]map[string]any)
	loadedRoles, rolesOK := row["roles"].([]map[string]any)
	if !profileOK || profile["bio"] != "profile" || !ordersOK || len(loadedOrders) != 1 || !rolesOK || len(loadedRoles) != 1 || loadedRoles[0]["name"] != "admin" {
		t.Fatalf("关闭期间未提交关联数据不可见: %#v", row)
	}
	if profile, err := users.HasOne(profiles, "user_id", int64(1)); err != nil || profile["bio"] != "profile" {
		t.Fatalf("关闭期间立即一对一关联失败: profile=%#v err=%v", profile, err)
	}
	if related, err := users.HasMany(orders, "user_id", int64(1)); err != nil || len(related) != 1 || related[0]["amount"] != int64(10) {
		t.Fatalf("关闭期间立即一对多关联失败: orders=%#v err=%v", related, err)
	}
	if related, err := users.BelongsToMany(roles, "drain_user_roles", "user_id", "role_id", int64(1)); err != nil || len(related) != 1 || related[0]["name"] != "admin" {
		t.Fatalf("关闭期间立即多对多关联或中间表查询失败: roles=%#v err=%v", related, err)
	}
	order, err := transactionOrders.With("user").FindMap()
	if err != nil {
		t.Fatalf("关闭期间反向关联预加载失败: %v", err)
	}
	if user, ok := order["user"].(map[string]any); !ok || user["name"] != "Ada" {
		t.Fatalf("关闭期间反向关联记录错误: %#v", order)
	}
	if user, err := transactionOrders.BelongsTo(NewModel(users.db, "drain_users"), int64(1), "id"); err != nil || user["name"] != "Ada" {
		t.Fatalf("关闭期间立即反向关联失败: user=%#v err=%v", user, err)
	}
}

// TestModelEndedTransactionImmediateRelationsNeverEscape 验证立即关联不能丢失已结束的根事务错误。
func TestModelEndedTransactionImmediateRelationsNeverEscape(t *testing.T) {
	database, _ := newModelScanSQLite(t)
	transaction, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	parent := NewModel(database, "scan_records").WithTx(transaction)
	related := NewModel(database, "scan_records")
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	queries := map[string]func() error{
		"has_one":         func() error { _, err := parent.HasOne(related, "id", 1); return err },
		"has_many":        func() error { _, err := parent.HasMany(related, "id", 1); return err },
		"belongs_to":      func() error { _, err := parent.BelongsTo(related, 1, "id"); return err },
		"belongs_to_many": func() error { _, err := parent.BelongsToMany(related, "scan_records", "id", "id", 1); return err },
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			if err := query(); !errors.Is(err, ErrTransactionDone) {
				t.Fatalf("立即关联逃逸到普通连接: %v", err)
			}
		})
	}
}
