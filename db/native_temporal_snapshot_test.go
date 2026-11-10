package db

import (
	"reflect"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j/dbtype"
)

func TestDatabaseSnapshotPreservesNativeTemporalTypes(t *testing.T) {
	location := time.FixedZone("source", 9*60*60)
	instant := time.Date(2026, time.November, 10, 12, 30, 0, 0, location)
	values := []interface{}{dbtype.Date(instant), dbtype.Time(instant), dbtype.LocalDateTime(instant), dbtype.LocalTime(instant)}
	connection := &modelBusinessConnection{}
	database := NewDB(connection)
	defer database.Close()
	for _, value := range values {
		cloned, err := snapshotDatabaseValue(value, time.UTC)
		if err != nil || reflect.TypeOf(cloned) != reflect.TypeOf(value) || !reflect.DeepEqual(cloned, value) {
			t.Fatalf("原生时间类型或时间组成被改变: %T %v", value, err)
		}
		request := newInsertRequest("events", map[string]interface{}{"at": value}, "id", false)
		data, err := request.DataSnapshot()
		if err != nil || !reflect.DeepEqual(data["at"], value) {
			t.Fatalf("写入请求拒绝原生时间: %T %v", value, err)
		}
		if _, err := database.Table("events").Where("at", value).Find(); err != nil {
			t.Fatalf("查询谓词拒绝原生时间: %T %v", value, err)
		}
		if len(connection.lastWhereArgs) != 1 || !reflect.DeepEqual(connection.lastWhereArgs[0], value) {
			t.Fatalf("驱动未收到原生时间值: %T %v", value, connection.lastWhereArgs)
		}
	}
}
