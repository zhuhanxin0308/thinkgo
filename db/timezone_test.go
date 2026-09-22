package db

import (
	"testing"
	"time"
)

// TestDatabaseImplicitTimezoneDefaultsToUTC 验证所有数据库隐式时间边界都不依赖宿主机 Local。
func TestDatabaseImplicitTimezoneDefaultsToUTC(t *testing.T) {
	var nilDatabase *DB
	if nilDatabase.Location() != time.UTC || nilDatabase.Now().Location() != time.UTC {
		t.Fatalf("空数据库必须回退 UTC: location=%v now=%v", nilDatabase.Location(), nilDatabase.Now().Location())
	}

	database := NewDB(&timestampCaptureConnection{})
	database.SetLocation(nil)
	if database.Location() != time.UTC || database.Now().Location() != time.UTC {
		t.Fatalf("数据库默认时区必须为 UTC: location=%v now=%v", database.Location(), database.Now().Location())
	}

	var nilQuery *Query
	if nilQuery.location() != time.UTC || nilQuery.now().Location() != time.UTC {
		t.Fatalf("空查询必须回退 UTC: location=%v now=%v", nilQuery.location(), nilQuery.now().Location())
	}

	var nilSQL *SQLConnection
	if nilSQL.Location() != time.UTC {
		t.Fatalf("空 SQL 连接必须回退 UTC: %v", nilSQL.Location())
	}
	sqlConnection := &SQLConnection{}
	sqlConnection.SetLocation(nil)
	if sqlConnection.Location() != time.UTC {
		t.Fatalf("SQL 连接默认时区必须为 UTC: %v", sqlConnection.Location())
	}

	instant := time.Date(2026, time.July, 25, 0, 30, 0, 0, time.FixedZone("deployment-local", 8*60*60))
	native, err := normalizeTimeValueInStorage(instant, nil, TimestampValueTypeNative)
	if err != nil {
		t.Fatalf("规范化原生时间失败: %v", err)
	}
	if normalized := native.(time.Time); normalized.Location() != time.UTC || !normalized.Equal(instant) {
		t.Fatalf("原生时间未规范化到 UTC: %#v", native)
	}
	written := normalizeDatabaseWriteMap(map[string]interface{}{"created_at": instant}, nil)
	if normalized := written["created_at"].(time.Time); normalized.Location() != time.UTC || !normalized.Equal(instant) {
		t.Fatalf("数据库写入时间未规范化到 UTC: %#v", written)
	}
}

// TestDatabaseNowUsesConfiguredLocation 验证数据库业务时钟会按配置的应用时区返回时间。
func TestDatabaseNowUsesConfiguredLocation(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载测试时区失败: %v", err)
	}

	database := NewDB(&timestampCaptureConnection{})
	database.SetLocation(location)
	database.clock = func() time.Time {
		return time.Date(2026, time.July, 24, 16, 30, 0, 0, time.UTC)
	}

	current := database.Now()
	if current.Location().String() != "Asia/Shanghai" {
		t.Fatalf("数据库当前时间时区错误: %q", current.Location())
	}
	if got := current.Format("2006-01-02 15:04:05"); got != "2026-07-25 00:30:00" {
		t.Fatalf("数据库当前时间错误: got=%q", got)
	}
}

// TestQueryBusinessTimesUseConfiguredLocation 验证自动时间戳和 WhereTime 使用数据库应用时区。
func TestQueryBusinessTimesUseConfiguredLocation(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载测试时区失败: %v", err)
	}

	connection := &timestampCaptureConnection{}
	database := NewDB(connection)
	database.SetLocation(location)
	database.autoTimestamp = true
	database.timestampValueType = TimestampValueTypeDateTime
	database.clock = func() time.Time {
		return time.Date(2026, time.July, 24, 16, 30, 0, 0, time.UTC)
	}

	if _, err := database.Table("users").Insert(map[string]interface{}{"username": "tester"}); err != nil {
		t.Fatalf("按应用时区写入自动时间戳失败: %v", err)
	}
	for _, field := range []string{"create_time", "update_time"} {
		if got := connection.insertData[field]; got != "2026-07-25 00:30:00" {
			t.Fatalf("%s 未使用应用时区: got=%#v", field, got)
		}
	}

	today := database.Table("users").WhereTime("create_time", "today")
	if today.err != nil {
		t.Fatalf("WhereTime(today) 失败: %v", today.err)
	}
	if got := today.args[0]; got != "2026-07-25 00:00:00" {
		t.Fatalf("WhereTime(today) 起点错误: got=%#v", got)
	}
	if got := today.args[1]; got != "2026-07-26 00:00:00" {
		t.Fatalf("WhereTime(today) 终点错误: got=%#v", got)
	}

	explicit := database.Table("users").WhereTime(
		"create_time",
		"=",
		time.Date(2026, time.July, 24, 16, 30, 0, 0, time.UTC),
	)
	if explicit.err != nil {
		t.Fatalf("显式时间条件失败: %v", explicit.err)
	}
	if got := explicit.args[0]; got != "2026-07-25 00:30:00" {
		t.Fatalf("显式时间条件未转换到应用时区: got=%#v", got)
	}
}

// TestQueryBusinessTimesRespectDaylightSavingTime 验证日历边界仍使用目标时区的 DST 规则。
func TestQueryBusinessTimesRespectDaylightSavingTime(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("加载测试时区失败: %v", err)
	}

	database := NewDB(&timestampCaptureConnection{})
	database.SetLocation(location)
	database.timestampValueType = TimestampValueTypeDateTime
	database.clock = func() time.Time {
		return time.Date(2026, time.March, 8, 6, 30, 0, 0, time.UTC)
	}

	today := database.Table("users").WhereTime("created_at", "today")
	if today.err != nil {
		t.Fatalf("DST 切换日 WhereTime(today) 失败: %v", today.err)
	}
	if got := today.args[0]; got != "2026-03-08 00:00:00" {
		t.Fatalf("DST 切换日起点错误: got=%#v", got)
	}
	if got := today.args[1]; got != "2026-03-09 00:00:00" {
		t.Fatalf("DST 切换日终点错误: got=%#v", got)
	}
}

// TestUnixTimeRangeUsesActualDSTDayLength 验证 Unix 日历范围使用真实的 DST 日长。
func TestUnixTimeRangeUsesActualDSTDayLength(t *testing.T) {
	location := mustTimezoneLocation(t, "America/New_York")
	database := NewDB(&timestampCaptureConnection{})
	database.SetLocation(location)
	database.timestampValueType = TimestampValueTypeUnix
	database.clock = func() time.Time {
		return time.Date(2026, time.March, 8, 6, 30, 0, 0, time.UTC)
	}

	query := database.Table("events").WhereTime("created_at", "today")
	if query.err != nil {
		t.Fatalf("DST Unix 日历查询失败: %v", query.err)
	}
	start, startOK := query.args[0].(int64)
	end, endOK := query.args[1].(int64)
	if !startOK || !endOK || end-start != int64(23*time.Hour/time.Second) {
		t.Fatalf("DST 日历范围未使用真实日长: args=%#v", query.args)
	}
}

// TestWhereTimeUsesDeclaredStorageType 验证同一个业务时刻在各存储契约下生成一致的绝对或墙上时间值。
func TestWhereTimeUsesDeclaredStorageType(t *testing.T) {
	location := mustTimezoneLocation(t, "Asia/Shanghai")
	database := NewDB(&timestampCaptureConnection{})
	database.SetLocation(location)
	database.clock = func() time.Time {
		return time.Date(2026, time.July, 24, 16, 30, 0, 0, time.UTC)
	}
	instant := time.Date(2026, time.July, 24, 16, 30, 0, 123000000, time.UTC)
	localInstant := instant.In(location)

	cases := []struct {
		name      string
		valueType string
		want      interface{}
	}{
		{name: "unix", valueType: TimestampValueTypeUnix, want: localInstant.Unix()},
		{name: "datetime", valueType: TimestampValueTypeDateTime, want: "2026-07-25 00:30:00"},
		{name: "date", valueType: TimestampValueTypeDate, want: "2026-07-25"},
		{name: "native", valueType: TimestampValueTypeNative, want: localInstant},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			query := database.Table("users").WhereTimeAs("created_at", testCase.valueType, "=", instant)
			if query.err != nil {
				t.Fatalf("WhereTimeAs 失败: %v", query.err)
			}
			if len(query.args) != 1 {
				t.Fatalf("时间参数数量错误: %#v", query.args)
			}
			switch expected := testCase.want.(type) {
			case time.Time:
				actual, ok := query.args[0].(time.Time)
				if !ok || !actual.Equal(expected) || actual.Location().String() != location.String() {
					t.Fatalf("原生时间参数错误: %#v", query.args[0])
				}
			default:
				if query.args[0] != expected {
					t.Fatalf("时间参数错误: got=%#v want=%#v", query.args[0], expected)
				}
			}
		})
	}

	rangeQuery := database.Table("users").WhereTimeAs("created_at", TimestampValueTypeUnix, "today")
	if rangeQuery.err != nil || rangeQuery.where[0] != "(created_at >= ? AND created_at < ?)" {
		t.Fatalf("Unix 日历查询契约错误: where=%#v err=%v", rangeQuery.where, rangeQuery.err)
	}
	start := time.Date(2026, time.July, 25, 0, 0, 0, 0, location)
	end := start.AddDate(0, 0, 1)
	if rangeQuery.args[0] != start.Unix() || rangeQuery.args[1] != end.Unix() {
		t.Fatalf("Unix 日历范围未使用半开区间: args=%#v", rangeQuery.args)
	}
}

// TestSoftDeleteUsesConfiguredLocation 验证软删除时间戳与普通更新时间戳使用同一应用时区。
func TestSoftDeleteUsesConfiguredLocation(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载测试时区失败: %v", err)
	}

	connection := &timestampCaptureConnection{}
	database := NewDB(connection)
	database.SetLocation(location)
	database.autoTimestamp = true
	database.timestampValueType = TimestampValueTypeDateTime
	database.clock = func() time.Time {
		return time.Date(2026, time.July, 24, 16, 30, 0, 0, time.UTC)
	}

	model := NewModel(database, "users").SoftDelete()
	if _, err := model.Where("id = ?", 1).Delete(); err != nil {
		t.Fatalf("软删除失败: %v", err)
	}
	if got := connection.updateData["delete_time"]; got != "2026-07-25 00:30:00" {
		t.Fatalf("delete_time 未使用应用时区: got=%#v", got)
	}
}

// TestCloneScannedTimeUsesConfiguredLocation 验证 SQL 查询结果中的 time.Time 使用数据库应用时区。
func TestCloneScannedTimeUsesConfiguredLocation(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载测试时区失败: %v", err)
	}

	value := cloneScannedValue(
		time.Date(2026, time.July, 24, 16, 30, 0, 0, time.UTC),
		location,
	)
	converted, ok := value.(time.Time)
	if !ok {
		t.Fatalf("查询时间值类型错误: %T", value)
	}
	if converted.Location().String() != "Asia/Shanghai" || converted.Format("2006-01-02 15:04:05") != "2026-07-25 00:30:00" {
		t.Fatalf("查询时间值未使用应用时区: %#v", converted)
	}
}

// TestPostgresNaiveTemporalScanKeepsWallClock 验证 PostgreSQL 无时区列不会被当作绝对时刻平移。
func TestPostgresNaiveTemporalScanKeepsWallClock(t *testing.T) {
	location := mustTimezoneLocation(t, "Asia/Shanghai")
	naive := time.Date(2026, time.July, 25, 0, 30, 0, 0, time.FixedZone("", 0))

	for _, databaseType := range []string{"DATE", "TIME", "TIMESTAMP", "TIMESTAMP WITHOUT TIME ZONE"} {
		t.Run(databaseType, func(t *testing.T) {
			converted := cloneScannedValueWithDatabaseType(naive, location, true, databaseType)
			actual, ok := converted.(time.Time)
			if !ok || actual.Location() != location || actual.Format(DefaultTimeFormat) != "2026-07-25 00:30:00" {
				t.Fatalf("PostgreSQL 无时区值被错误转换: type=%s value=%#v", databaseType, converted)
			}
		})
	}

	absolute := cloneScannedValueWithDatabaseType(naive, location, true, "TIMESTAMPTZ")
	actual, ok := absolute.(time.Time)
	if !ok || !actual.Equal(naive) || actual.Location() != location {
		t.Fatalf("PostgreSQL 有时区值不应按墙上时间处理: %#v", absolute)
	}
}

// TestNormalizeDatabaseWriteTimeUsesConfiguredLocation 验证写入边界统一时间表示但不改变绝对时刻。
func TestNormalizeDatabaseWriteTimeUsesConfiguredLocation(t *testing.T) {
	location := mustTimezoneLocation(t, "Asia/Shanghai")
	instant := time.Date(2026, time.July, 24, 16, 30, 0, 123000000, time.UTC)
	nested := map[string]interface{}{
		"time":  instant,
		"items": []interface{}{instant},
	}
	data := normalizeDatabaseWriteMap(map[string]interface{}{"happened_at": instant, "nested": nested}, location)

	actual, ok := data["happened_at"].(time.Time)
	if !ok || !actual.Equal(instant) || actual.Location() != location {
		t.Fatalf("写入时间未统一到应用时区: %#v", data["happened_at"])
	}
	actualNested := data["nested"].(map[string]interface{})
	actualItem := actualNested["items"].([]interface{})[0].(time.Time)
	if !actualItem.Equal(instant) || actualItem.Location() != location {
		t.Fatalf("嵌套写入时间未统一到应用时区: %#v", actualItem)
	}
}

func mustTimezoneLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("加载测试时区失败: %v", err)
	}
	return location
}
