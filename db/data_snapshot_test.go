package db

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestDatabaseSnapshotPreservesValuesAndOwnership 覆盖命名容器、结构体、共享子图和标准数据库值。
func TestDatabaseSnapshotPreservesValuesAndOwnership(t *testing.T) {
	type namedBytes []byte
	type namedPointer *namedBytes
	type document struct {
		Bytes         namedBytes
		Map           map[string][]byte
		Items         [1][]byte
		Time          time.Time
		Decimal       bson.Decimal128
		privateScalar string
	}
	instant := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	decimal := bson.NewDecimal128(1, 2)
	input := &document{Bytes: namedBytes("a"), Map: map[string][]byte{"a": []byte("b")}, Items: [1][]byte{[]byte("c")}, Time: instant, Decimal: decimal, privateScalar: "保留"}
	root := map[string]interface{}{"left": input, "right": input, "bson": bson.D{{Key: "data", Value: bson.Binary{Data: []byte("d")}}}}
	cloned, err := cloneDatabaseMap(root)
	if err != nil {
		t.Fatal(err)
	}
	left := cloned["left"].(*document)
	if !reflect.DeepEqual(left, input) || left == input || left != cloned["right"].(*document) {
		t.Fatal("快照必须保留值和共享子图关系，同时隔离原对象")
	}
	left.Bytes[0], left.Map["a"][0], left.Items[0][0] = 'X', 'X', 'X'
	if string(input.Bytes) != "a" || string(input.Map["a"]) != "b" || string(input.Items[0]) != "c" {
		t.Fatal("结构体内部数据未隔离")
	}
	cloned["bson"].(bson.D)[0].Value.(bson.Binary).Data[0] = 'X'
	if string(root["bson"].(bson.D)[0].Value.(bson.Binary).Data) != "d" {
		t.Fatal("BSON 内部数据未隔离")
	}
	bytes := namedBytes("value")
	var pointer namedPointer = &bytes
	for _, value := range []interface{}{nil, (*document)(nil), map[string]int(nil), []interface{}(nil), []interface{}{}, pointer, bson.NewObjectID(), [2]byte{1, 2}} {
		copy, err := cloneDatabaseValue(value)
		if err != nil || !reflect.DeepEqual(copy, value) || reflect.TypeOf(copy) != reflect.TypeOf(value) {
			t.Fatalf("值或类型未保留: %T %v", value, err)
		}
	}
	location := time.FixedZone("test-zone", 8*60*60)
	normalized, err := normalizeDatabaseWriteMap(root, location)
	if err != nil {
		t.Fatal(err)
	}
	converted := normalized["left"].(*document).Time
	if !converted.Equal(instant) || converted.Location() != location || input.Time.Location() != time.UTC {
		t.Fatal("结构体时间转换污染输入或丢失绝对时刻")
	}
}

// TestDatabaseSnapshotRejectsUncopyableGraphs 验证无法隔离的数据明确失败，不返回部分有效快照。
func TestDatabaseSnapshotRejectsUncopyableGraphs(t *testing.T) {
	type privateState struct{ data []byte }
	type linked struct{ Next *linked }
	cycle := &linked{}
	cycle.Next = cycle
	var deep interface{} = "leaf"
	for index := 0; index < maxDatabaseValueDepth; index++ {
		deep = []interface{}{deep}
	}
	oversized := make([]int, maxDatabaseValueNodes+1)
	key := "mutable"
	for _, value := range []interface{}{privateState{data: []byte("x")}, cycle, deep, oversized, map[*string]int{&key: 1}, make(chan int), func() {}} {
		copy, err := cloneDatabaseValue(value)
		if !errors.Is(err, ErrInvalidQuery) || copy != nil {
			t.Fatalf("非法 %T 未拒绝: %v", value, err)
		}
		if _, err := normalizeDatabaseWriteMap(map[string]interface{}{"value": value}, nil); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("时区规范化未共享安全边界: %T %v", value, err)
		}
	}
	// 大量已完成的共享节点不应重复复制；只有当前递归路径的重复引用才是环。
	var dag interface{} = []byte("leaf")
	const sharedLevels = 30
	for index := 0; index < sharedLevels; index++ {
		dag = []interface{}{dag, dag}
	}
	if _, err := cloneDatabaseValue(dag); err != nil {
		t.Fatalf("合法共享子图被误拒绝: %v", err)
	}
	// 不重叠的切片视图可以共享首地址，身份还必须包含长度。
	views := make([]interface{}, 2)
	views[1] = views[:1]
	if _, err := cloneDatabaseValue(views); err != nil {
		t.Fatalf("切片视图误判为环: %v", err)
	}
}

// TestDatabaseSnapshotRejectsAtEveryWriteBoundary 验证回调和直接请求都不能绕过参数安全边界。
func TestDatabaseSnapshotRejectsAtEveryWriteBoundary(t *testing.T) {
	cycle := map[string]interface{}{}
	cycle["self"] = cycle
	connection := &modelBusinessConnection{}
	database := NewDB(connection)
	t.Cleanup(func() { _ = database.Close() })
	model := NewModel(database, "items")
	for _, event := range []ModelEventType{ModelBeforeInsert, ModelBeforeUpdate} {
		if err := model.On(event, func(data map[string]interface{}) bool { data["cycle"] = cycle; return true }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := model.Insert(map[string]interface{}{"name": "valid"}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("插入回调未拒绝: %v", err)
	}
	if _, err := model.newModelQuery().Where("id", 1).Update(map[string]interface{}{"name": "valid"}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("更新回调未拒绝: %v", err)
	}
	if connection.lastContext != nil {
		t.Fatal("非法回调结果到达驱动")
	}
	insert := NewInsertRequest("items", cycle, "id", false)
	if _, err := insert.DataSnapshot(); !errors.Is(err, ErrInvalidQuery) || insert.Data() != nil {
		t.Fatal("插入请求丢失构造错误")
	}
	update := NewUpdateRequest("items", cycle, Predicate{}, "id")
	if _, err := update.DataSnapshot(); !errors.Is(err, ErrInvalidQuery) || update.Data() != nil {
		t.Fatal("更新请求丢失构造错误")
	}
	if _, err := CloneDriverDataChecked(cycle); !errors.Is(err, ErrInvalidQuery) || CloneDriverData(cycle) != nil {
		t.Fatal("独立驱动复制入口未拒绝环")
	}
	if err := ValidateDriverData(cycle); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal("独立驱动校验入口未拒绝环")
	}
	for _, query := range []*Query{
		database.Table("items").Where(map[string]interface{}{"value": cycle}),
		database.Table("items").Where([][]interface{}{{"value", "=", cycle}}),
		database.Table("items").Where(func(group *ConditionGroup) { group.Where("value", cycle) }),
		database.Table("items").WhereIn("value", []interface{}{cycle}),
		database.Table("items").WhereBetween("value", cycle, 1),
		database.Table("items").HavingRaw("value = ?", cycle),
		database.Table("items").HavingField("value", "=", cycle),
	} {
		if _, _, err := query.BuildSelectSQL(); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("条件入口未拒绝环: %v", err)
		}
	}
	if _, err := ParsePredicate([]string{"value = ?"}, []interface{}{cycle}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("驱动条件入口未拒绝环: %v", err)
	}
}
