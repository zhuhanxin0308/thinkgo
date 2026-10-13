package db_test

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

// This example compiles a query only; it neither connects nor authenticates a user.
func ExampleQuery_WhereRaw_boundValues() {
	database := db.NewDB(&db.SQLConnection{Builder: &builder.Sqlite{}})
	defer database.Close()
	submitted := "x' OR 1=1 --"
	statement, args, err := database.Table("users").
		WhereRaw("tenant_id = ? AND name = ?", int64(7), submitted).
		BuildSelectSQL()
	if err != nil {
		panic(err)
	}
	fmt.Println("input embedded in SQL:", strings.Contains(statement, submitted))
	fmt.Println("bound tenant:", args[0])
	fmt.Println("bound name preserved:", args[1] == submitted)
	// Output:
	// input embedded in SQL: false
	// bound tenant: 7
	// bound name preserved: true
}

func TestRawSQLBoundaryCompilationKeepsValuesSeparate(t *testing.T) {
	dialects := map[string]db.Builder{
		"sqlite": &builder.Sqlite{}, "mysql": &builder.Mysql{},
		"postgres": &builder.Pgsql{}, "sqlserver": &builder.Sqlsrv{},
	}
	for name, dialect := range dialects {
		t.Run(name, func(t *testing.T) {
			database := db.NewDB(&db.SQLConnection{Builder: dialect})
			defer database.Close()
			compile := func(input string) (string, []interface{}, error) {
				return database.Table("raw_records").Field("id").
					WhereRaw("tenant_id = ? AND name = ?", int64(7), input).
					Group("id, name").HavingRaw("name = ? AND COUNT(*) = ?", input, int64(1)).
					BuildSelectSQL()
			}
			baseline, _, err := compile("ordinary")
			if err != nil {
				t.Fatal(err)
			}
			for _, input := range []string{"x' OR 1=1 --", "x'; DROP TABLE raw_records; --", "? :1 $1 @p1 /* ? */", "load_file('x'); sleep(1)", "资料 O'Reilly\\end"} {
				statement, args, err := compile(input)
				if err != nil || statement != baseline || dialect.Rebind(statement) != dialect.Rebind(baseline) {
					t.Fatalf("input changed SQL structure: %q %v", statement, err)
				}
				if !reflect.DeepEqual(args, []interface{}{int64(7), input, input, int64(1)}) || strings.Contains(statement, input) {
					t.Fatalf("input was not preserved as bound data: %#v %s", args, statement)
				}
			}
		})
	}
}

func TestRawSQLBoundaryArgumentsAreSnapshots(t *testing.T) {
	database := db.NewDB(&db.SQLConnection{Builder: &builder.Sqlite{}})
	defer database.Close()
	source := []byte("original")
	query := database.Table("raw_records").WhereRaw("name = ?", source)
	source[0] = 'X'
	_, args, err := query.BuildSelectSQL()
	if err != nil || len(args) != 1 {
		t.Fatalf("build: %#v %v", args, err)
	}
	value, ok := args[0].([]byte)
	if !ok || !bytes.Equal(value, []byte("original")) {
		t.Fatalf("input alias leaked: %#v", args)
	}
	value[0] = 'Y'
	_, again, err := query.BuildSelectSQL()
	if err != nil || !reflect.DeepEqual(again, []interface{}{[]byte("original")}) {
		t.Fatalf("returned argument aliases query state: %#v %v", again, err)
	}
}

func TestRawSQLBoundaryRejectsIncompleteBindings(t *testing.T) {
	database := db.NewDB(&db.SQLConnection{Builder: &builder.Sqlite{}})
	defer database.Close()
	for _, clause := range []string{"", "name = ?\x00", "name = 'unterminated", "name = ? AND id = ?"} {
		for _, query := range []*db.Query{
			database.Table("raw_records").WhereRaw(clause, "value"),
			database.Table("raw_records").Group("name").HavingRaw(clause, "value"),
		} {
			if statement, _, err := query.BuildSelectSQL(); err == nil {
				t.Fatalf("invalid raw clause compiled: %q => %s", clause, statement)
			}
		}
	}
}

// Nested containers are compilation fixtures, not a claim every SQL driver accepts maps.
func TestBuildSelectSQLNestedArgumentOwnership(t *testing.T) {
	database := db.NewDB(&db.SQLConnection{Builder: &builder.Sqlite{}})
	defer database.Close()
	tenant := int64(7)
	nested := map[string]interface{}{"payload": []byte("original")}
	query := database.Table("raw_records").WhereRaw("metadata = ?", nested).
		Group("id").HavingRaw("id = ?", &tenant)
	originalSQL, first, err := query.BuildSelectSQL()
	if err != nil {
		t.Fatal(err)
	}
	first[0].(map[string]interface{})["payload"].([]byte)[0] = 'X'
	*first[1].(*int64) = 8
	laterSQL, later, err := query.BuildSelectSQL()
	if err != nil || laterSQL != originalSQL || !reflect.DeepEqual(later, []interface{}{nested, &tenant}) {
		t.Fatalf("mutable output corrupted query: %#v %v", later, err)
	}
	var nilBytes []byte
	empty := []byte{}
	_, args, err := database.Table("raw_records").WhereRaw("a = ? AND b = ?", nilBytes, empty).BuildSelectSQL()
	if err != nil || args[0].([]byte) != nil || args[1].([]byte) == nil {
		t.Fatalf("nil/empty contract changed: %#v %v", args, err)
	}
}

func TestBuildSelectSQLConcurrentOutputMutation(t *testing.T) {
	database := db.NewDB(&db.SQLConnection{Builder: &builder.Sqlite{}})
	defer database.Close()
	query := database.Table("raw_records").WhereRaw("name = ?", []byte("original"))
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 40 {
				_, args, err := query.BuildSelectSQL()
				if err != nil {
					t.Error(err)
					return
				}
				value := args[0].([]byte)
				if string(value) != "original" {
					t.Errorf("query value mutated: %q", value)
					return
				}
				value[0] = 'X'
			}
		}()
	}
	wg.Wait()
}

func BenchmarkRawSQLBoundaryBuild(b *testing.B) {
	for _, name := range []string{"scalar", "bytes"} {
		b.Run(name, func(b *testing.B) {
			database := db.NewDB(&db.SQLConnection{Builder: &builder.Sqlite{}})
			defer database.Close()
			var value interface{} = "original"
			if name == "bytes" {
				value = bytes.Repeat([]byte("x"), 128)
			}
			query := database.Table("raw_records").WhereRaw("tenant_id = ? AND name = ?", int64(7), value)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := query.BuildSelectSQL(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
