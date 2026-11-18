package contract

import (
	"database/sql"
	"reflect"
	"testing"
)

func TestOracleExplicitKeyDestinationPreservesBindingType(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		want  any
	}{
		{name: "字符", value: "001", want: new(string)},
		{name: "数值", value: int64(3), want: new(int64)},
		{name: "空字符串", value: ""},
		{name: "驱动值类型未知", value: sql.NullString{String: "001", Valid: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := OracleExplicitKeyDestination(map[string]any{"Name": "Ada", "account_id": test.value}, "ACCOUNT_ID")
			if reflect.TypeOf(got) != reflect.TypeOf(test.want) {
				t.Fatalf("Oracle 回传绑定类型错误: 得到 %T，期望 %T", got, test.want)
			}
		})
	}
}
