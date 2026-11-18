package db

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

// oracleUppercaseTestBuilder 在默认无 oracle 构建标签环境下模拟 Oracle 方言的大写标识符引用与 :N 占位符，
// 避免直接引用受 //go:build oracle 约束的 builder.Oracle 类型。
type oracleUppercaseTestBuilder struct{}

func (o *oracleUppercaseTestBuilder) DialectName() string { return "oracle" }

func (o *oracleUppercaseTestBuilder) QuoteIdentifier(name string) string {
	parts := strings.Split(strings.ToUpper(strings.TrimSpace(name)), ".")
	for i, part := range parts {
		if part == "*" {
			continue
		}
		parts[i] = `"` + strings.ReplaceAll(part, `"`, `""`) + `"`
	}
	return strings.Join(parts, ".")
}

func (o *oracleUppercaseTestBuilder) QuoteFields(fields string) string {
	trimmed := strings.TrimSpace(fields)
	if trimmed == "*" {
		return "*"
	}
	items := strings.Split(trimmed, ",")
	out := make([]string, 0, len(items))
	for _, raw := range items {
		item := strings.TrimSpace(raw)
		expr, alias := splitIdentifierAlias(item)
		var quoted string
		switch {
		case expr == "1":
			quoted = "1"
		case strings.Contains(expr, "(") && strings.HasSuffix(expr, ")"):
			open := strings.IndexByte(expr, '(')
			fn := strings.ToUpper(strings.TrimSpace(expr[:open]))
			arg := strings.TrimSpace(expr[open+1 : len(expr)-1])
			quoted = fn + "(" + o.QuoteIdentifier(arg) + ")"
		default:
			quoted = o.QuoteIdentifier(expr)
		}
		if alias != "" {
			quoted += " AS " + o.QuoteIdentifier(alias)
		}
		out = append(out, quoted)
	}
	return strings.Join(out, ", ")
}

func (o *oracleUppercaseTestBuilder) Rebind(query string) string {
	var b strings.Builder
	param := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			param++
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(param))
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}

func (o *oracleUppercaseTestBuilder) Select(table, fields string, where []string, order string, limit, offset int) string {
	q := fmt.Sprintf("SELECT %s FROM %s", o.QuoteFields(fields), o.QuoteIdentifier(table))
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	orderClause, limitClause := o.Pagination(order, limit, offset)
	return q + orderClause + limitClause
}

func (o *oracleUppercaseTestBuilder) Pagination(order string, limit, offset int) (string, string) {
	orderClause := ""
	if strings.TrimSpace(order) != "" {
		parts := strings.Fields(order)
		orderClause = " ORDER BY " + o.QuoteIdentifier(parts[0])
		if len(parts) == 2 {
			orderClause += " " + strings.ToUpper(parts[1])
		}
	}
	limitClause := ""
	if limit > 0 || offset > 0 {
		limitClause = fmt.Sprintf(" OFFSET %d ROWS", offset)
		if limit > 0 {
			limitClause += fmt.Sprintf(" FETCH NEXT %d ROWS ONLY", limit)
		}
	}
	return orderClause, limitClause
}

func (o *oracleUppercaseTestBuilder) Lock(mode LockMode) (LockSpec, error) {
	switch mode {
	case LockNone:
		return LockSpec{}, nil
	case LockForUpdate:
		return LockSpec{Tail: " FOR UPDATE"}, nil
	default:
		return LockSpec{}, ErrUnsupportedFeature
	}
}

func (o *oracleUppercaseTestBuilder) Insert(table string, data map[string]interface{}) (string, []interface{}) {
	keys := sortedDatabaseKeys(data)
	cols := make([]string, len(keys))
	marks := make([]string, len(keys))
	vals := make([]interface{}, len(keys))
	for i, k := range keys {
		cols[i] = o.QuoteIdentifier(k)
		marks[i] = "?"
		vals[i] = data[k]
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", o.QuoteIdentifier(table), strings.Join(cols, ", "), strings.Join(marks, ", ")), vals
}

func (o *oracleUppercaseTestBuilder) InsertReturning(string, map[string]interface{}, string) (string, []interface{}, bool) {
	return "", nil, false
}

func (o *oracleUppercaseTestBuilder) Update(table string, data map[string]interface{}, where []string) (string, []interface{}) {
	keys := sortedDatabaseKeys(data)
	sets := make([]string, len(keys))
	vals := make([]interface{}, len(keys))
	for i, k := range keys {
		sets[i] = o.QuoteIdentifier(k) + " = ?"
		vals[i] = data[k]
	}
	q := fmt.Sprintf("UPDATE %s SET %s", o.QuoteIdentifier(table), strings.Join(sets, ", "))
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	return q, vals
}

func (o *oracleUppercaseTestBuilder) Delete(table string, where []string) string {
	q := "DELETE FROM " + o.QuoteIdentifier(table)
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	return q
}

func (o *oracleUppercaseTestBuilder) Count(table string, where []string) string {
	q := "SELECT COUNT(*) FROM " + o.QuoteIdentifier(table)
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	return q
}

func (o *oracleUppercaseTestBuilder) SupportsLastInsertId() bool { return false }
func (o *oracleUppercaseTestBuilder) MaxBindParams() int         { return 999 }

// TestWhereExpPreservesSQLTemporalConstantsAcrossDialects 验证 WhereExp 将标准零参时间关键字保留为表达式而非列名 (#591)。
func TestWhereExpPreservesSQLTemporalConstantsAcrossDialects(t *testing.T) {
	dialects := []struct {
		name      string
		build     Builder
		quotedCol string
	}{
		{"sqlite", &builder.Sqlite{}, `"expired_at"`},
		{"postgres", &builder.Pgsql{}, `"expired_at"`},
		{"mysql", &builder.Mysql{}, "`expired_at`"},
		{"sqlserver", &builder.Sqlsrv{}, `[expired_at]`},
		{"oracle", &oracleUppercaseTestBuilder{}, `"EXPIRED_AT"`},
	}

	constants := []string{"current_timestamp", "CURRENT_TIMESTAMP", "current_date", "CURRENT_DATE", "current_time", "CURRENT_TIME"}
	for _, d := range dialects {
		t.Run(d.name, func(t *testing.T) {
			database := newCursorProjectionTestDB(t, d.build, func(string, []driver.NamedValue) ([]string, [][]driver.Value, error) {
				return nil, nil, nil
			})
			for _, kw := range constants {
				sqlText, args, err := database.Name("tokens").WhereExp("expired_at", "<=", kw).BuildSelectSQL()
				if err != nil || len(args) != 0 {
					t.Fatalf("WhereExp(%q) 构建失败: sql=%q args=%v err=%v", kw, sqlText, args, err)
				}
				wantFragment := d.quotedCol + " <= " + kw
				if !strings.Contains(sqlText, wantFragment) {
					t.Fatalf("WhereExp(%q) 误将时间常量加了列名引号: got=%q wantFragment=%q", kw, sqlText, wantFragment)
				}
			}

			groupSQL, groupArgs, err := database.Name("tokens").Where(func(g *ConditionGroup) {
				g.WhereExp("expired_at", "<=", "current_timestamp")
			}).BuildSelectSQL()
			if err != nil || len(groupArgs) != 0 || !strings.Contains(groupSQL, d.quotedCol+" <= current_timestamp") {
				t.Fatalf("闭包条件组 WhereExp 时间常量构建错误: sql=%q args=%v err=%v", groupSQL, groupArgs, err)
			}

			// 显式调用 WhereColumn 时仍按列名引用。
			colSQL, _, err := database.Name("tokens").WhereColumn("expired_at", "<=", "current_timestamp").BuildSelectSQL()
			if err != nil || strings.HasSuffix(colSQL, "<= current_timestamp") {
				t.Fatalf("WhereColumn 应始终按列标识符加引号: sql=%q err=%v", colSQL, err)
			}
		})
	}
}

// TestCountAdvancedNamesDerivedConstantColumnForSQLServer 验证高级 Count 子查询为常量列指定别名 (#591)。
func TestCountAdvancedNamesDerivedConstantColumnForSQLServer(t *testing.T) {
	dialects := []struct {
		name     string
		build    Builder
		expected string
	}{
		{"sqlserver", &builder.Sqlsrv{}, "SELECT COUNT(*) AS tg_count FROM (SELECT 1 AS [tg_one] FROM [orders] GROUP BY [user_id]) tg_count_sub"},
		{"postgres", &builder.Pgsql{}, `SELECT COUNT(*) AS tg_count FROM (SELECT 1 AS "tg_one" FROM "orders" GROUP BY "user_id") tg_count_sub`},
		{"mysql", &builder.Mysql{}, "SELECT COUNT(*) AS tg_count FROM (SELECT 1 AS `tg_one` FROM `orders` GROUP BY `user_id`) tg_count_sub"},
		{"sqlite", &builder.Sqlite{}, `SELECT COUNT(*) AS tg_count FROM (SELECT 1 AS "tg_one" FROM "orders" GROUP BY "user_id") tg_count_sub`},
		{"oracle", &oracleUppercaseTestBuilder{}, `SELECT COUNT(*) AS tg_count FROM (SELECT 1 AS "TG_ONE" FROM "ORDERS" GROUP BY "USER_ID") tg_count_sub`},
	}

	for _, d := range dialects {
		t.Run(d.name, func(t *testing.T) {
			var captured string
			database := newCursorProjectionTestDB(t, d.build, func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
				captured = query
				return []string{"tg_count"}, [][]driver.Value{{int64(4)}}, nil
			})
			total, err := database.Name("orders").Group("user_id").Count()
			if err != nil || total != 4 {
				t.Fatalf("高级 Count 执行失败: total=%d err=%v", total, err)
			}
			if captured != d.expected {
				t.Fatalf("高级 Count SQL 不符:\ngot:  %s\nwant: %s", captured, d.expected)
			}
		})
	}
}

// TestHavingNullCompilationAndOrPrecedence 验证 Having/HavingField 的 NULL 编译及顶层 OR 括号保护 (#591)。
func TestHavingNullCompilationAndOrPrecedence(t *testing.T) {
	pgDB := newCursorProjectionTestDB(t, &builder.Pgsql{}, func(string, []driver.NamedValue) ([]string, [][]driver.Value, error) {
		return nil, nil, nil
	})

	var typedNil *int64
	validCases := []struct {
		name     string
		query    *Query
		wantSQL  string
		wantArgs []interface{}
	}{
		{
			name:    "Having 聚合三元组 IS nil",
			query:   pgDB.Name("orders").Group("user_id").Having("MAX(deleted_at)", "IS", nil),
			wantSQL: `HAVING MAX("deleted_at") IS NULL`,
		},
		{
			name:    "Having 聚合参数化 IS ?",
			query:   pgDB.Name("orders").Group("user_id").Having("MAX(deleted_at) IS ?", nil),
			wantSQL: `HAVING MAX("deleted_at") IS NULL`,
		},
		{
			name:    "Having 速记 nil",
			query:   pgDB.Name("orders").Group("user_id").Having("deleted_at", typedNil),
			wantSQL: `HAVING "deleted_at" IS NULL`,
		},
		{
			name:    "Having 三元组 IS NOT nil",
			query:   pgDB.Name("orders").Group("user_id").Having("MAX(deleted_at)", "IS NOT", nil),
			wantSQL: `HAVING MAX("deleted_at") IS NOT NULL`,
		},
		{
			name:    "HavingField 等号 nil",
			query:   pgDB.Name("orders").Group("user_id").HavingField("deleted_at", "=", nil),
			wantSQL: `HAVING "deleted_at" IS NULL`,
		},
		{
			name:    "HavingField 不等于 nil",
			query:   pgDB.Name("orders").Group("user_id").HavingField("deleted_at", "!=", nil),
			wantSQL: `HAVING "deleted_at" IS NOT NULL`,
		},
		{
			name:     "HavingRaw 顶层 OR 组合 HavingField 自动包裹括号",
			query:    pgDB.Name("orders").Group("user_id").HavingRaw("SUM(a) > ? OR SUM(b) > ?", 1, 2).HavingField("user_id", ">", 10),
			wantSQL:  `HAVING (SUM(a) > ? OR SUM(b) > ?) AND "user_id" > ?`,
			wantArgs: []interface{}{1, 2, 10},
		},
		{
			name:     "HavingRaw 字符串内 OR 不误加括号",
			query:    pgDB.Name("orders").Group("user_id").HavingRaw("MAX(tag) = 'A OR B'").HavingField("user_id", ">", 10),
			wantSQL:  `HAVING MAX(tag) = 'A OR B' AND "user_id" > ?`,
			wantArgs: []interface{}{10},
		},
		{
			name:     "Having 顶层布尔 OR 组合 HavingField 自动包裹括号",
			query:    pgDB.Name("orders").Group("user_id").Having("COUNT(*) > ? OR SUM(amount) > ?", 2, 100).HavingField("MAX(deleted_at)", "IS", nil),
			wantSQL:  `HAVING (COUNT(*) > ? OR SUM("amount") > ?) AND MAX("deleted_at") IS NULL`,
			wantArgs: []interface{}{2, 100},
		},
		{
			name:     "Having COUNT DISTINCT 与 BETWEEN",
			query:    pgDB.Name("orders").Group("user_id").Having("COUNT(DISTINCT category_id) BETWEEN ? AND ?", 1, 5),
			wantSQL:  `HAVING COUNT(DISTINCT "category_id") BETWEEN ? AND ?`,
			wantArgs: []interface{}{1, 5},
		},
	}

	for _, tc := range validCases {
		t.Run(tc.name, func(t *testing.T) {
			sqlText, args, err := tc.query.BuildSelectSQL()
			if err != nil {
				t.Fatalf("构造 HAVING SQL 失败: %v", err)
			}
			if !strings.Contains(sqlText, tc.wantSQL) {
				t.Fatalf("HAVING SQL 不匹配:\ngot:  %s\nwant: %s", sqlText, tc.wantSQL)
			}
			rebound := (&builder.Pgsql{}).Rebind(sqlText)
			if strings.Contains(rebound, "IS $") {
				t.Fatalf("PostgreSQL 重绑定后不应出现非法 IS $N 语法: %s", rebound)
			}
			if len(args) != len(tc.wantArgs) || (len(args) > 0 && !reflect.DeepEqual(args, tc.wantArgs)) {
				t.Fatalf("HAVING 绑定参数不匹配: got=%#v want=%#v", args, tc.wantArgs)
			}
		})
	}

	invalidQueries := []*Query{
		pgDB.Name("orders").Group("user_id").Having("COUNT(*)", ">", nil),
		pgDB.Name("orders").Group("user_id").Having("total >= ?", nil),
		pgDB.Name("orders").Group("user_id").Having("total BETWEEN ? AND ?", nil, 10),
		pgDB.Name("orders").Group("user_id").HavingField("total", ">", nil),
		pgDB.Name("orders").Group("user_id").Having("SUM(*) > ?", 1),
		pgDB.Name("orders").Group("user_id").Having("SLEEP(1) > ?", 0),
	}
	for index, invalid := range invalidQueries {
		if _, _, err := invalid.BuildSelectSQL(); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("第 %d 个非法 HAVING 查询应返回 ErrInvalidQuery，实际为 %v", index, err)
		}
	}
}

type oracleUserDTO struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Score int64  `json:"score"`
}

type oracleUserRecord struct {
	*Model
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Score int64  `json:"score"`
}

// TestOracleUppercaseResultColumnsWorkAcrossQueryAndORM 验证 Oracle 返回大写列名时 Value/Column/SeekPage/ChunkById/ORM 均正常读取 (#592)。
func TestOracleUppercaseResultColumnsWorkAcrossQueryAndORM(t *testing.T) {
	type storedRow struct {
		id    int64
		name  string
		score int64
	}
	rowsData := []storedRow{
		{id: 1, name: "Alice", score: 10},
		{id: 2, name: "Bob", score: 20},
		{id: 3, name: "Carol", score: 30},
	}

	database := newCursorProjectionTestDB(t, &oracleUppercaseTestBuilder{}, func(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, `SELECT "NAME" FROM "USERS"`):
			if strings.Contains(query, `WHERE "ID" = :1`) {
				return []string{"NAME"}, [][]driver.Value{{"Alice"}}, nil
			}
			return []string{"NAME"}, [][]driver.Value{{"Alice"}, {"Bob"}, {"Carol"}}, nil
		case strings.Contains(query, `SELECT DISTINCT "NAME", "ID" FROM "USERS"`),
			strings.Contains(query, `SELECT "NAME", "ID" FROM "USERS"`):
			return []string{"NAME", "ID"}, [][]driver.Value{{"Alice", int64(1)}, {"Bob", int64(2)}, {"Carol", int64(3)}}, nil
		case strings.Contains(query, `AS "ITEM_ID"`):
			var after int64
			if len(args) == 1 {
				after = args[0].Value.(int64)
			}
			var out [][]driver.Value
			for _, r := range rowsData {
				if r.id > after {
					out = append(out, []driver.Value{r.id, r.name})
					if len(out) == 3 {
						break
					}
				}
			}
			return []string{"ITEM_ID", "NAME"}, out, nil
		default:
			var after int64
			if len(args) == 1 && strings.Contains(query, `> :1`) {
				after = args[0].Value.(int64)
			}
			if strings.Contains(query, `WHERE "ID" = :1`) && len(args) == 1 {
				target := args[0].Value.(int64)
				for _, r := range rowsData {
					if r.id == target {
						return []string{"ID", "NAME", "SCORE"}, [][]driver.Value{{r.id, r.name, r.score}}, nil
					}
				}
				return []string{"ID", "NAME", "SCORE"}, nil, nil
			}
			var out [][]driver.Value
			for _, r := range rowsData {
				if r.id > after {
					out = append(out, []driver.Value{r.id, r.name, r.score})
				}
			}
			if strings.Contains(query, "FETCH NEXT 1 ROWS ONLY") && len(out) > 1 {
				out = out[:1]
			} else if strings.Contains(query, "FETCH NEXT 3 ROWS ONLY") && len(out) > 3 {
				out = out[:3]
			}
			return []string{"ID", "NAME", "SCORE"}, out, nil
		}
	})

	val, err := database.Name("users").Where("id", int64(1)).Value("name")
	if err != nil || val != "Alice" {
		t.Fatalf("Oracle Value 读取失败: val=%v err=%v", val, err)
	}

	colList, err := database.Name("users").Column("name")
	if err != nil || !reflect.DeepEqual(colList, []interface{}{"Alice", "Bob", "Carol"}) {
		t.Fatalf("Oracle 直接列扫描 Column 失败: got=%#v err=%v", colList, err)
	}

	colMap, err := database.Name("users").Column("name", "id")
	if err != nil || !reflect.DeepEqual(colMap, map[string]interface{}{"1": "Alice", "2": "Bob", "3": "Carol"}) {
		t.Fatalf("Oracle 带 key 直接列扫描失败: got=%#v err=%v", colMap, err)
	}

	streamMap, err := database.Name("users").Distinct().Column("name", "id")
	if err != nil || !reflect.DeepEqual(streamMap, map[string]interface{}{"1": "Alice", "2": "Bob", "3": "Carol"}) {
		t.Fatalf("Oracle 流式列扫描失败: got=%#v err=%v", streamMap, err)
	}

	page, err := database.Name("users").SeekPage(2, "id", nil)
	if err != nil || !page.HasMore || page.NextCursor != int64(2) {
		t.Fatalf("Oracle SeekPage 默认主键读取失败: page=%+v err=%v", page, err)
	}

	aliasPage, err := database.Name("users").Field("id AS item_id, name").SeekPage(2, "id", nil)
	if err != nil || !aliasPage.HasMore || aliasPage.NextCursor != int64(2) {
		t.Fatalf("Oracle SeekPage 列别名读取失败: page=%+v err=%v", aliasPage, err)
	}

	model := NewModel(database, "users")
	var single oracleUserDTO
	found, err := model.Where("id", int64(1)).Find(&single)
	if err != nil || !found || single.ID != 1 || single.Name != "Alice" || single.Score != 10 {
		t.Fatalf("Oracle Model.Find DTO 失败: found=%t single=%+v err=%v", found, single, err)
	}

	var list []oracleUserDTO
	if err := model.Query().Select(&list); err != nil || len(list) != 3 || list[1].Name != "Bob" {
		t.Fatalf("Oracle Model.Select DTO 失败: list=%+v err=%v", list, err)
	}

	typedPage, err := SeekPage[oracleUserDTO](model.Query(), 2, nil)
	if err != nil || !typedPage.HasMore || typedPage.NextCursor != int64(2) || len(typedPage.List) != 2 || typedPage.List[0].Name != "Alice" {
		t.Fatalf("Oracle 类型化 SeekPage 失败: page=%+v err=%v", typedPage, err)
	}

	var activeRecord oracleUserRecord
	found, err = model.Where("id", int64(1)).Find(&activeRecord)
	if err != nil || !found || activeRecord.ID != 1 || activeRecord.Model == nil || activeRecord.record == nil {
		t.Fatalf("Oracle 活动记录加载失败: found=%t record=%+v err=%v", found, activeRecord, err)
	}
	if activeRecord.record.identity != int64(1) || !activeRecord.record.loaded["id"] || !activeRecord.record.loaded["name"] {
		t.Fatalf("Oracle 活动记录未归一化主键与已加载列集: identity=%v loaded=%v", activeRecord.record.identity, activeRecord.record.loaded)
	}

	// 验证非 Oracle 方言（如 PostgreSQL）不会把大写别名 ID 误当作小写主键 id，且名为 distinct 的列名仍正常加引号
	pgDB := newCursorProjectionTestDB(t, &builder.Pgsql{}, func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		return []string{"ID", "name"}, [][]driver.Value{{int64(999), "Ada"}}, nil
	})
	pgModel := NewModel(pgDB, "users")
	var pgRec oracleUserRecord
	found, err = pgModel.Field("external_id AS ID, name").Find(&pgRec)
	if err != nil || !found || pgRec.ID != 0 {
		t.Fatalf("PostgreSQL 大写别名 ID 不应映射到小写主键字段 id: found=%t rec=%+v err=%v", found, pgRec, err)
	}
	if err := pgRec.Save(); !errors.Is(err, ErrModelIdentityMissing) {
		t.Fatalf("PostgreSQL 未加载真实主键 id 时 Save 必须返回 ErrModelIdentityMissing，实际为 %v", err)
	}

	distinctSQL, distinctArgs, err := pgDB.Name("metrics").Group("distinct").HavingField("distinct", ">", 1).HavingField("COUNT(DISTINCT distinct)", ">=", 2).BuildSelectSQL()
	if err != nil || len(distinctArgs) != 2 {
		t.Fatalf("构造 distinct 列名 HAVING SQL 失败: sql=%q args=%v err=%v", distinctSQL, distinctArgs, err)
	}
	if !strings.Contains(distinctSQL, `HAVING "distinct" > ? AND COUNT(DISTINCT "distinct") >= ?`) {
		t.Fatalf("名为 distinct 的列未正确加引号或 DISTINCT 修饰符被误引: %s", distinctSQL)
	}
}
