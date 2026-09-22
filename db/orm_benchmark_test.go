package db

import (
	"database/sql"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"

	_ "github.com/mattn/go-sqlite3"
)

type benchmarkORMUser struct {
	ID        int64     `thinkgo:"id,omitempty"`
	Name      string    `thinkgo:"name"`
	Email     string    `thinkgo:"email"`
	Score     int64     `thinkgo:"score"`
	Status    int       `thinkgo:"status"`
	CreatedAt time.Time `thinkgo:"created_at"`
	UpdatedAt time.Time `thinkgo:"updated_at,omitempty"`
	Ignored   string    `thinkgo:"-"`
}

func benchmarkSQLite(b *testing.B, size int) *DB {
	b.Helper()
	raw, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	if _, err := raw.Exec(`CREATE TABLE bench_orm_users (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		score INTEGER NOT NULL,
		created_at TEXT NOT NULL
	)`); err != nil {
		raw.Close()
		b.Fatal(err)
	}
	tx, err := raw.Begin()
	if err != nil {
		raw.Close()
		b.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO bench_orm_users(id,name,score,created_at) VALUES(?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		raw.Close()
		b.Fatal(err)
	}
	for index := 1; index <= size; index++ {
		if _, err := statement.Exec(index, "user-"+strconv.Itoa(index), index%100, "2026-07-14 00:00:00"); err != nil {
			statement.Close()
			tx.Rollback()
			raw.Close()
			b.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		tx.Rollback()
		raw.Close()
		b.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		raw.Close()
		b.Fatal(err)
	}
	database := NewDB(&SQLConnection{DB: raw, Builder: &builder.Sqlite{}})
	b.Cleanup(func() {
		if err := database.Close(); err != nil {
			b.Errorf("close benchmark SQLite: %v", err)
		}
	})
	return database
}

func BenchmarkScanRowsSQLite(b *testing.B) {
	for _, size := range []int{1_000, 10_000, 100_000} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			database := benchmarkSQLite(b, size)
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				rows, err := database.Query("SELECT id,name,score,created_at FROM bench_orm_users ORDER BY id")
				if err != nil || len(rows) != size {
					b.Fatalf("rows=%d want=%d err=%v", len(rows), size, err)
				}
			}
		})
	}
}

// BenchmarkColumnSQLite 量化 Column 只保留目标值时的大结果集物化成本。
func BenchmarkColumnSQLite(b *testing.B) {
	for _, size := range []int{1_000, 10_000, 100_000} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			database := benchmarkSQLite(b, size)
			query := database.Table("bench_orm_users").Order("id")
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				values, err := query.Column("name")
				list, ok := values.([]interface{})
				if err != nil || !ok || len(list) != size {
					b.Fatalf("Column 结果错误: type=%T rows=%d want=%d err=%v", values, len(list), size, err)
				}
			}
		})
	}
}

func BenchmarkStructToMap(b *testing.B) {
	model := NewModel(nil, "bench_orm_users")
	value := &benchmarkORMUser{
		ID:        7,
		Name:      "Ada",
		Email:     "ada@example.test",
		Score:     99,
		Status:    1,
		CreatedAt: time.Unix(1_752_422_400, 0),
	}
	b.ReportAllocs()
	for iteration := 0; iteration < b.N; iteration++ {
		data, err := model.structToMap(value)
		if err != nil || len(data) != 6 {
			b.Fatalf("data=%#v err=%v", data, err)
		}
	}
}

func BenchmarkGetterPipeline(b *testing.B) {
	model := NewModel(nil, "bench_orm_users")
	for _, field := range []string{"name", "score", "status", "created_at"} {
		field := field
		if err := model.Getter(field, func(value interface{}, row map[string]interface{}) interface{} {
			if _, exists := row["id"]; !exists {
				return nil
			}
			return value
		}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	for iteration := 0; iteration < b.N; iteration++ {
		row := map[string]interface{}{
			"id": int64(7), "name": "Ada", "score": int64(99), "status": 1,
			"created_at": "2026-07-14 00:00:00",
		}
		if result := model.applyGetters(row); len(result) != 5 {
			b.Fatalf("unexpected getter result: %#v", result)
		}
	}
}

func BenchmarkRelationIndex(b *testing.B) {
	for _, parents := range []int{1_000, 10_000} {
		b.Run(strconv.Itoa(parents), func(b *testing.B) {
			rows := make([]relationRow, 0, parents*4)
			for parent := 1; parent <= parents; parent++ {
				for child := 0; child < 4; child++ {
					rows = append(rows, relationRow{
						data:    map[string]interface{}{"id": int64(parent*4 + child), "user_id": int64(parent)},
						rawKeys: map[string]interface{}{"user_id": int64(parent)},
					})
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				grouped, err := groupRawRelationRows(rows, "user_id")
				if err != nil || len(grouped) != parents {
					b.Fatalf("groups=%d want=%d err=%v", len(grouped), parents, err)
				}
			}
		})
	}
}

func BenchmarkChunkPaginationSQLite(b *testing.B) {
	for _, size := range []int{100_000, 1_000_000} {
		for _, mode := range []string{"offset", "keyset"} {
			b.Run(fmt.Sprintf("%s/%d", mode, size), func(b *testing.B) {
				database := benchmarkSQLite(b, size)
				b.ReportAllocs()
				b.ResetTimer()
				for iteration := 0; iteration < b.N; iteration++ {
					query := database.Table("bench_orm_users").Field("id,name,score,created_at").Order("id ASC").Limit(1_000)
					if mode == "offset" {
						query = query.Offset(size - 1_000)
					} else {
						query = query.WhereField("id", ">", size-1_000)
					}
					rows, err := query.Select()
					if err != nil || len(rows) != 1_000 {
						b.Fatalf("rows=%d want=1000 err=%v", len(rows), err)
					}
				}
			})
		}
	}
}

// BenchmarkSeekPageSQLite 衡量真实游标分页终端方法在深游标位置的物化与校验成本。
func BenchmarkSeekPageSQLite(b *testing.B) {
	for _, size := range []int{100_000, 1_000_000} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			database := benchmarkSQLite(b, size)
			query := database.Table("bench_orm_users").Field("id,name,score,created_at")
			after := int64(size - 1_000)
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				page, err := query.SeekPage(1_000, "id", after)
				if err != nil {
					b.Fatalf("游标分页失败: %v", err)
				}
				if len(page.List) != 1_000 {
					b.Fatalf("游标分页行数错误: rows=%d", len(page.List))
				}
				if page.List[0]["id"] != after+1 {
					b.Fatalf("游标分页首行错误: first=%#v", page.List[0])
				}
			}
		})
	}
}

// BenchmarkConditionChainConstruction 衡量批量条件在 Query 与 ModelQuery 中的构造分配。
func BenchmarkConditionChainConstruction(b *testing.B) {
	database := NewDB(&relationMockConnection{})
	model := NewModel(database, "bench_orm_users")
	for _, size := range []int{10, 100, 1_000} {
		conditions := make([][]interface{}, size)
		for index := range conditions {
			conditions[index] = []interface{}{"status", "=", index % 5}
		}

		b.Run(fmt.Sprintf("query/%d", size), func(b *testing.B) {
			base := database.Table("bench_orm_users")
			b.ReportAllocs()
			for iteration := 0; iteration < b.N; iteration++ {
				query := base.WhereFields(conditions)
				if len(query.where) != size {
					b.Fatalf("where=%d want=%d", len(query.where), size)
				}
			}
		})

		b.Run(fmt.Sprintf("model/%d", size), func(b *testing.B) {
			base := model.newModelQuery()
			b.ReportAllocs()
			for iteration := 0; iteration < b.N; iteration++ {
				query := base.WhereFields(conditions)
				if len(query.query.where) != size {
					b.Fatalf("where=%d want=%d", len(query.query.where), size)
				}
			}
		})
	}
}

// BenchmarkOrderedCursorCompare 衡量默认整数游标比较的分配成本。
func BenchmarkOrderedCursorCompare(b *testing.B) {
	left := int64(42)
	right := uint64(43)
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		comparison, err := compareOrderedDatabaseCursor(left, right)
		if err != nil || comparison >= 0 {
			b.Fatalf("游标比较错误: comparison=%d err=%v", comparison, err)
		}
	}
}
