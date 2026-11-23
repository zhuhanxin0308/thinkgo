package database

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// 直接装入旧行，验证读取时只恢复超出浮点范围的顶层整数。
func TestDBCacheStoredNumberCompatibility(t *testing.T) {
	conn := newRecordingCacheConn()
	store, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatal(err)
	}
	maxFloat := strconv.FormatFloat(math.MaxFloat64, 'f', 0, 64)
	overflow := strings.Repeat("9", len(maxFloat))
	for _, tc := range []struct {
		name    string
		raw     string
		want    interface{}
		corrupt bool
	}{
		{name: "float-limit", raw: maxFloat, want: float64(math.MaxFloat64)},
		{name: "rounded-int64", raw: strconv.FormatInt(math.MaxInt64, 10), want: float64(math.MaxInt64)},
		{name: "signed-whitespace", raw: " \r\n-" + overflow + "\t", want: json.Number("-" + overflow)},
		{name: "quoted-number", raw: strconv.Quote(overflow), want: overflow},
		{name: "finite-exponent", raw: "2.5e3", want: float64(2500)},
		{name: "nested-number", raw: `{"value":42}`, want: map[string]interface{}{"value": float64(42)}},
		{name: "null", raw: "null"},
		{name: "fraction-overflow", raw: overflow + ".0", corrupt: true},
		{name: "exponent-overflow", raw: "1e400", corrupt: true},
		{name: "array-overflow", raw: "[" + overflow + "]", corrupt: true},
		{name: "object-overflow", raw: `{"value":` + overflow + "}", corrupt: true},
		{name: "leading-zero", raw: "0" + overflow, corrupt: true},
		{name: "trailing-document", raw: overflow + " null", corrupt: true},
		{name: "invalid-whitespace", raw: "\u00a0" + overflow, corrupt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := mustStorageKey(t, store, tc.name)
			conn.rows[key] = map[string]interface{}{"key": key, "value": []byte(tc.raw), "expiry": int64(0)}
			value, found, err := store.Get(tc.name)
			if tc.corrupt {
				if found || value != nil || !errors.Is(err, ErrCorruptCacheEntry) {
					t.Fatalf("损坏记录的读取结果为 %v, %t, %v", value, found, err)
				}
				return
			}
			if err != nil || !found || !reflect.DeepEqual(value, tc.want) {
				t.Fatalf("读取结果为 %v (%T), %t, %v，预期 %v (%T)", value, value, found, err, tc.want, tc.want)
			}
		})
	}
}
