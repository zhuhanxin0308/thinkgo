package db_test

import (
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

func ExampleQuery_WhereLikeLiteral() {
	// Only compile a predicate: the example does not open a database connection.
	database := db.NewDB(&db.SQLConnection{Builder: &builder.Pgsql{}})
	query := database.Table("users").Where("tenant_id", 7).WhereLikeLiteral("name", "100%_done")
	predicate, err := query.Predicate()
	if err != nil {
		panic(err)
	}
	for _, clause := range predicate.Clauses() {
		fmt.Println(clause.SQL, clause.Args)
	}
	// Output:
	// "tenant_id" = ? [7]
	// "name" LIKE ? ESCAPE '!' [100!%!_done]
}
