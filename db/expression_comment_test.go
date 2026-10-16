package db

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

func TestExpressionCommentBoundary(t *testing.T) {
	for _, dialect := range []Builder{&builder.Sqlite{}, &builder.Mysql{}, &builder.Pgsql{}, &builder.Sqlsrv{}} {
		t.Run(dialect.DialectName(), func(t *testing.T) {
			assertExpressionCommentBoundary(t, dialect)
		})
	}
}

func assertExpressionCommentBoundary(t *testing.T, dialect Builder) {
	t.Helper()
	database := NewDB(&SQLConnection{Builder: dialect})
	t.Cleanup(func() { _ = database.Close() })
	for _, expression := range []string{
		"0 -- ", "0--", "0---1", "0 --\n1", "0 --\r\n1",
		"0/*", "0/**/", "0/*1*/", "0/*+1*/", "0/*!1*/", "0*/", "0 #", "0#1",
	} {
		t.Run("reject/"+expression, func(t *testing.T) {
			queries := map[string]*Query{
				"where_exp": database.Table("docs").WhereExp("id", ">", expression),
				"group": database.Table("docs").Where(func(group *ConditionGroup) {
					group.WhereExp("id", ">", expression)
				}),
				"where_text": database.Table("docs").Where("id > " + expression),
			}
			for name, query := range queries {
				query = query.Where("deleted_at IS NULL").Limit(2)
				statement, _, err := query.BuildSelectSQL()
				if !errors.Is(err, ErrInvalidQuery) || statement != "" {
					t.Errorf("%s admitted comment %q: sql=%q err=%v", name, expression, statement, err)
				}
			}
		})
	}
	for _, test := range []struct {
		name       string
		expression string
		args       []interface{}
	}{
		{name: "negative_literal", expression: "-1"},
		{name: "subtraction", expression: "3 - 2"},
		{name: "spaced_negative", expression: "0 - -1"},
		{name: "tab_separated_negative", expression: "0 -\t-1"},
		{name: "parenthesized_negative", expression: "0-(-1)"},
		{name: "negative_parameter", expression: "ABS(?)", args: []interface{}{-1}},
		{name: "negated_parameter", expression: "-ABS(?)", args: []interface{}{-1}},
		{name: "division", expression: "4 / 2"},
		{name: "multiplication", expression: "2 * 1"},
	} {
		t.Run("allow/"+test.name, func(t *testing.T) {
			statement, args, err := database.Table("docs").Field("id").
				WhereExp("id", ">", test.expression, test.args...).Where("deleted_at IS NULL").
				Order("id ASC").Limit(2).BuildSelectSQL()
			if err != nil {
				t.Fatal(err)
			}
			wantPredicate := dialect.QuoteIdentifier("id") + " > " + test.expression + " AND " + dialect.QuoteIdentifier("deleted_at") + " IS NULL"
			_, wantLimit := dialect.Pagination("id ASC", 2, 0)
			if !strings.Contains(statement, wantPredicate) || !strings.HasSuffix(statement, wantLimit) || len(args) != len(test.args) || len(args) > 0 && !reflect.DeepEqual(args, test.args) {
				t.Fatalf("arithmetic changed filters, limit or parameters: sql=%q args=%#v", statement, args)
			}
		})
	}
}
