package mongo

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestLiteralLikeNativePredicate(t *testing.T) {
	for _, literal := range []string{"", "%", "_", "[ab]", "a.b*", `\E.*`, "客户_%😀", "line\n", "line"} {
		connection := &MongoConnection{}
		predicate, err := db.NewDB(connection).Table("users").WhereLikeLiteral("name", literal).Predicate()
		if err != nil {
			t.Fatal(err)
		}
		filter, err := connection.compilePredicate(predicate, "_id", "id")
		if err != nil {
			t.Fatal(err)
		}
		expression, ok := filter["name"].(bson.M)
		if !ok {
			t.Fatalf("missing literal expression: %#v", filter)
		}
		pattern, ok := expression["$regex"].(string)
		if !ok || expression["$options"] != "i" {
			t.Fatal("lost regex/LIKE case contract")
		}
		if !strings.HasPrefix(pattern, `\A`) || !strings.HasSuffix(pattern, `\z`) {
			t.Fatal("non-absolute anchors")
		}
		matcher, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			t.Fatal(err)
		}
		if !matcher.MatchString(literal) || matcher.MatchString(literal+"x") || matcher.MatchString("x"+literal) || matcher.MatchString(literal+"\n") {
			t.Fatalf("literal %q expanded", literal)
		}
	}
	for _, values := range [][]any{nil, {"x", "y"}, {1}, {strings.Repeat("x", maxMongoLikeLength+1)}} {
		if _, err := compileMongoNode(PredicateNode{Kind: PredicateLikeLiteral, Field: "name", Values: values}); !errors.Is(err, db.ErrInvalidQuery) {
			t.Fatalf("invalid native node accepted: %v", err)
		}
	}
}
