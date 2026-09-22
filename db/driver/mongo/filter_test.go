package mongo

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestMongoPrimaryKeyObjectIDRoundTripOnly(t *testing.T) {
	id := bson.NewObjectID()
	connection := &MongoConnection{}
	request := newSelectRequest(
		"users",
		"name",
		mustTestPredicate(t, []string{"_id = ?"}, []interface{}{id.Hex()}),
		"_id",
		"",
		0,
		0,
		nil,
	)
	filter, findOptions, err := connection.mongoFindParts(request)
	if err != nil {
		t.Fatal(err)
	}
	if filter["_id"] != id {
		t.Fatalf("declared primary key was not converted to ObjectID: %#v", filter)
	}
	resolved := &options.FindOptions{}
	for _, apply := range findOptions.List() {
		if err := apply(resolved); err != nil {
			t.Fatal(err)
		}
	}
	projection, ok := resolved.Projection.(bson.M)
	if !ok || projection["name"] != 1 || projection["_id"] != 0 {
		t.Fatalf("explicit projection must exclude unselected _id: %#v", resolved.Projection)
	}

	plainRequest := newSelectRequest(
		"users",
		"*",
		mustTestPredicate(t, []string{"external_code = ?"}, []interface{}{id.Hex()}),
		"_id",
		"",
		0,
		0,
		nil,
	)
	plainFilter, _, err := connection.mongoFindParts(plainRequest)
	if err != nil {
		t.Fatal(err)
	}
	if plainFilter["external_code"] != id.Hex() {
		t.Fatalf("ordinary string field was incorrectly converted: %#v", plainFilter)
	}

	secondID := bson.NewObjectID()
	inRequest := newSelectRequest(
		"users",
		"*",
		mustTestPredicate(t, []string{"(_id = ? OR _id IN (?, ?))"}, []interface{}{id.Hex(), id.Hex(), secondID.Hex()}),
		"_id",
		"",
		0,
		0,
		nil,
	)
	inFilter, _, err := connection.mongoFindParts(inRequest)
	if err != nil {
		t.Fatal(err)
	}
	alternatives, ok := inFilter["$or"].([]bson.M)
	if !ok || len(alternatives) != 2 || alternatives[0]["_id"] != id {
		t.Fatalf("nested primary-key conditions were not converted: %#v", inFilter)
	}
	condition, ok := alternatives[1]["_id"].(bson.M)
	values, valuesOK := condition["$in"].([]interface{})
	if !ok || !valuesOK || len(values) != 2 || values[0] != id || values[1] != secondID {
		t.Fatalf("primary-key IN values were not converted: %#v", inFilter)
	}

	invalidRequest := newSelectRequest(
		"users", "*", mustTestPredicate(t, []string{"_id = ?"}, []interface{}{"not-an-object-id"}), "_id", "", 0, 0, nil,
	)
	if _, _, err := connection.mongoFindParts(invalidRequest); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("invalid primary-key text must return ErrInvalidQuery: %v", err)
	}
}

func TestMongoIntrinsicIDRoundTripsWhenQueryUsesDefaultPrimaryKey(t *testing.T) {
	id := bson.NewObjectID()
	connection := &MongoConnection{}
	filter, err := connection.buildFilterWithPrimaryKey(
		[]string{"_id = ?"},
		[]interface{}{id.Hex()},
		"id",
	)
	if err != nil {
		t.Fatal(err)
	}
	if filter["_id"] != id {
		t.Fatalf("Mongo intrinsic _id was not restored to ObjectID: %#v", filter)
	}
	row := normalizeMongoDocument(bson.M{"_id": id, "owner_id": id}, "id")
	if row["_id"] != id.Hex() || row["owner_id"] != id {
		t.Fatalf("Mongo intrinsic _id normalization escaped its path: %#v", row)
	}
}

func TestMongoNormalizationOnlyStringifiesPrimaryKey(t *testing.T) {
	id := bson.NewObjectID()
	ownerID := bson.NewObjectID()
	nestedID := bson.NewObjectID()
	row := normalizeMongoDocument(bson.M{
		"_id":      id,
		"owner_id": ownerID,
		"profile":  bson.M{"avatar_id": nestedID},
	}, "_id")
	if row["_id"] != id.Hex() || row["owner_id"] != ownerID {
		t.Fatalf("ObjectID normalization escaped the primary-key path: %#v", row)
	}
	profile, ok := row["profile"].(map[string]interface{})
	if !ok || !reflect.DeepEqual(profile["avatar_id"], nestedID) {
		t.Fatalf("nested ObjectID must retain its BSON type: %#v", row["profile"])
	}
}

// TestMongoInPlaceNormalizationPreservesPublicShape 验证原地规范化不会改变对外字段语义。
func TestMongoInPlaceNormalizationPreservesPublicShape(t *testing.T) {
	id := bson.NewObjectID()
	document := bson.M{
		"_id":  id,
		"name": "Ada",
	}
	row := normalizeMongoDocumentInPlaceWithAliasInLocation(document, "_id", "id", time.Local)
	if row["id"] != id.Hex() || row["name"] != "Ada" {
		t.Fatalf("原地规范化结果错误: %#v", row)
	}
	if _, exists := row["_id"]; exists {
		t.Fatalf("逻辑主键结果不应泄露存储键: %#v", row)
	}
	if _, exists := document["_id"]; exists {
		t.Fatalf("原地规范化后存储键应被移除: %#v", document)
	}
}

// TestMongoBuildFilterRejectsUnparseable 验证无法解析的条件会返回错误，
// 而非静默丢弃导致空 filter 引发全表更新/删除。
func TestMongoBuildFilterRejectsUnparseable(t *testing.T) {
	c := &MongoConnection{}

	// 受支持的条件应正常解析。
	filter, err := c.buildFilter([]string{"age >= ?"}, []interface{}{18})
	if err != nil {
		t.Fatalf("受支持条件不应报错: %v", err)
	}
	if len(filter) != 1 {
		t.Fatalf("filter 应包含 1 个条件，实际 %d", len(filter))
	}

	// 无法解析的条件必须报错，避免退化为空 filter。
	if _, err := c.buildFilter([]string{"weird_unparseable_clause"}, nil); err == nil {
		t.Fatalf("无法解析的条件应返回错误，避免空 filter 全表操作")
	}

	// 参数数量不足也应报错而非吞掉条件。
	if _, err := c.buildFilter([]string{"age = ?"}, nil); err == nil {
		t.Fatalf("参数不足应返回错误")
	}
}

// TestMongoBuildFilterRejectsExtraArgsAndUnsafeFields 验证所有参数都被消费且字段不能成为操作符键。
func TestMongoBuildFilterRejectsExtraArgsAndUnsafeFields(t *testing.T) {
	connection := &MongoConnection{}
	if _, err := connection.buildFilter([]string{"age = ?"}, []interface{}{18, 19}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("多余参数应返回 ErrInvalidQuery，实际为 %v", err)
	}
	if _, err := connection.buildFilter([]string{"$where = ?"}, []interface{}{"sleep(1)"}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("操作符字段应返回 ErrInvalidQuery，实际为 %v", err)
	}
}

// TestMongoScalarValidationRejectsPointerBypassAndAllowsDriverScalars 验证指针不能绕过文档值检查。
func TestMongoScalarValidationRejectsPointerBypassAndAllowsDriverScalars(t *testing.T) {
	document := map[string]interface{}{"$ne": nil}
	if err := requireMongoScalar(&document); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("指向 map 的指针应被拒绝，实际为 %v", err)
	}
	for _, value := range []interface{}{time.Now(), bson.NewObjectID(), bson.DateTime(1), []byte("binary")} {
		if err := requireMongoScalar(value); err != nil {
			t.Fatalf("合法 Mongo 标量 %T 被拒绝: %v", value, err)
		}
	}
}

// TestMongoBuildFilterPreservesDuplicateFieldPredicates 验证上下界不会因 map 覆盖而丢失。
func TestMongoBuildFilterPreservesDuplicateFieldPredicates(t *testing.T) {
	connection := &MongoConnection{}
	filter, err := connection.buildFilter([]string{"age >= ?", "age <= ?"}, []interface{}{18, 30})
	if err != nil {
		t.Fatalf("构建范围过滤失败: %v", err)
	}
	rangeFilter, ok := filter["age"].(bson.M)
	if !ok || rangeFilter["$gte"] != 18 || rangeFilter["$lte"] != 30 {
		t.Fatalf("范围条件被覆盖: %#v", filter)
	}
}

// TestMongoBuildFilterSupportsOrAndTrueImpossiblePredicate 验证 OR 与空 IN 的恒假语义。
func TestMongoBuildFilterSupportsOrAndTrueImpossiblePredicate(t *testing.T) {
	connection := &MongoConnection{}
	filter, err := connection.buildFilter([]string{"(status = ? OR owner_id = ?)"}, []interface{}{"open", int64(7)})
	if err != nil {
		t.Fatalf("构建 OR 过滤失败: %v", err)
	}
	if alternatives, ok := filter["$or"].([]bson.M); !ok || len(alternatives) != 2 {
		t.Fatalf("OR 过滤结构错误: %#v", filter)
	}
	impossible, err := connection.buildFilter([]string{"1 = 0"}, nil)
	if err != nil {
		t.Fatalf("构建恒假过滤失败: %v", err)
	}
	if _, ok := impossible["$expr"]; !ok {
		t.Fatalf("空 IN 必须生成真正恒假的 $expr，实际为 %#v", impossible)
	}
}

// TestMongoBuildFilterParsesNotInAsNin 验证 NOT IN 不会被提前解析成错误字段的 IN 条件。
func TestMongoBuildFilterParsesNotInAsNin(t *testing.T) {
	c := &MongoConnection{}

	filter, err := c.buildFilter([]string{"status NOT IN (?, ?)"}, []interface{}{"draft", "deleted"})
	if err != nil {
		t.Fatalf("NOT IN 条件应可解析: %v", err)
	}

	condition, ok := filter["status"].(bson.M)
	if !ok {
		t.Fatalf("NOT IN 应绑定到 status 字段，实际 filter=%#v", filter)
	}
	values, ok := condition["$nin"].([]interface{})
	if !ok || len(values) != 2 || values[0] != "draft" || values[1] != "deleted" {
		t.Fatalf("NOT IN 应生成 $nin 条件，实际 %#v", condition)
	}
	if _, exists := filter["status NOT"]; exists {
		t.Fatalf("NOT IN 不应被解析成错误字段 status NOT，实际 filter=%#v", filter)
	}
}

// TestMongoBuildFilterParsesNotLikeAsNegatedRegex 验证 NOT LIKE 不会被提前解析成错误字段的 LIKE 条件。
func TestMongoBuildFilterParsesNotLikeAsNegatedRegex(t *testing.T) {
	c := &MongoConnection{}

	filter, err := c.buildFilter([]string{"name NOT LIKE ?"}, []interface{}{"%admin%"})
	if err != nil {
		t.Fatalf("NOT LIKE 条件应可解析: %v", err)
	}

	condition, ok := filter["name"].(bson.M)
	if !ok {
		t.Fatalf("NOT LIKE 应绑定到 name 字段，实际 filter=%#v", filter)
	}
	negated, ok := condition["$not"].(bson.M)
	if !ok {
		t.Fatalf("NOT LIKE 应生成 $not 正则条件，实际 %#v", condition)
	}
	if negated["$regex"] != "^.*admin.*$" || negated["$options"] != "i" {
		t.Fatalf("NOT LIKE 正则条件不正确，实际 %#v", negated)
	}
	if _, exists := filter["name NOT"]; exists {
		t.Fatalf("NOT LIKE 不应被解析成错误字段 name NOT，实际 filter=%#v", filter)
	}
}

// TestMongoBuildFilterCoversNullComparisonAndNestedAnd 验证空值、全部比较符及嵌套 AND
// 都生成 MongoDB 原生操作符，非法尾部、非标量和超长模式则被拒绝。
func TestMongoBuildFilterCoversNullComparisonAndNestedAnd(t *testing.T) {
	connection := &MongoConnection{}
	nullFilter, err := connection.buildFilter([]string{"deleted_at IS NULL", "email IS NOT NULL"}, nil)
	if err != nil {
		t.Fatalf("构建 MongoDB 空值过滤失败: %v", err)
	}
	if nullFilter["deleted_at"] != nil {
		t.Fatalf("IS NULL 过滤错误: %#v", nullFilter)
	}
	if condition, ok := nullFilter["email"].(bson.M); !ok || condition["$ne"] != nil {
		t.Fatalf("IS NOT NULL 过滤错误: %#v", nullFilter["email"])
	}

	filter, err := connection.buildFilter(
		[]string{"(score <> ? AND level > ?)", "rank < ?", "quota != ?"},
		[]interface{}{0, 1, 10, 20},
	)
	if err != nil {
		t.Fatalf("构建 MongoDB 完整比较过滤失败: %v", err)
	}
	encoded, err := bson.MarshalExtJSON(filter, false, false)
	if err != nil {
		t.Fatalf("编码 MongoDB 比较过滤失败: %v", err)
	}
	for _, field := range []string{"score", "level", "rank", "quota"} {
		if !strings.Contains(string(encoded), `"`+field+`"`) {
			t.Fatalf("比较过滤缺少字段 %q: %s", field, encoded)
		}
	}

	invalid := []struct {
		where string
		args  []interface{}
	}{
		{where: "id IN ?", args: []interface{}{1}},
		{where: "name LIKE (?, ?)", args: []interface{}{"a", "b"}},
		{where: "payload = ?", args: []interface{}{map[string]interface{}{"$gt": 0}}},
		{where: "name LIKE ?", args: []interface{}{strings.Repeat("a", maxMongoLikeLength+1)}},
		{where: "(id = ?", args: []interface{}{1}},
		{where: "id = ? OR ", args: []interface{}{1}},
	}
	for index, testCase := range invalid {
		if _, err := connection.buildFilter([]string{testCase.where}, testCase.args); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("第 %d 个非法 MongoDB 条件应返回 ErrInvalidQuery，实际为 %v", index, err)
		}
	}
}

// TestNormalizeMongoValueCopiesNestedBinary 验证 BSON 规范化递归处理 map、数组和字节，
// 返回的二进制值不与驱动缓冲区共享内存。
func TestNormalizeMongoValueCopiesNestedBinary(t *testing.T) {
	binary := []byte{1, 2, 3}
	driverBinary := bson.Binary{Subtype: 0x80, Data: []byte{4, 5, 6}}
	value := normalizeMongoValue(map[string]interface{}{
		"items": []interface{}{bson.NewObjectID(), binary, driverBinary},
	})
	normalized := value.(map[string]interface{})
	items := normalized["items"].([]interface{})
	copyValue := items[1].([]byte)
	binary[0] = 9
	if copyValue[0] != 1 {
		t.Fatalf("规范化二进制不得共享底层数组: %#v", copyValue)
	}
	normalizedDriverBinary := items[2].(bson.Binary)
	driverBinary.Data[0] = 9
	if normalizedDriverBinary.Data[0] != 4 || normalizedDriverBinary.Subtype != 0x80 {
		t.Fatalf("驱动 Binary 必须复制数据并保留子类型: %#v", normalizedDriverBinary)
	}
}
