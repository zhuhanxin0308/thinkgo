package db

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestDatabaseWriteNormalizesNamedTemporalContainers 验证命名容器中的时间也统一到应用时区。
func TestDatabaseWriteNormalizesNamedTemporalContainers(t *testing.T) {
	location := mustTimezoneLocation(t, "Asia/Shanghai")
	instant := time.Date(2026, time.July, 24, 16, 30, 0, 0, time.UTC)
	data := normalizeDatabaseWriteMap(map[string]interface{}{
		"document": bson.M{"happened_at": instant}, "items": bson.A{instant},
	}, location)
	for _, value := range []interface{}{data["document"].(bson.M)["happened_at"], data["items"].(bson.A)[0]} {
		actual, ok := value.(time.Time)
		if !ok || !actual.Equal(instant) || actual.Location() != location {
			t.Fatalf("命名容器时间未归一化: %#v", value)
		}
	}
}
