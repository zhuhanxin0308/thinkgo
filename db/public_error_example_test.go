package db_test

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

func ExampleSanitizeError() {
	// 模拟数据库返回值；实际代码先用原始错误完成事务和部分写入处理。
	raw := errors.New("SQLSTATE 23505: private constraint customer_email_unique")
	public := db.SanitizeError(raw)
	if public == nil {
		return
	}
	payload, err := json.Marshal(map[string]string{
		"code": "database_error", "message": public.Error(),
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(string(payload))
	fmt.Println("original cause exposed:", errors.Is(public, raw))
	fmt.Println("nil preserved:", db.SanitizeError(nil) == nil)
	// Output:
	// {"code":"database_error","message":"数据库操作失败"}
	// original cause exposed: false
	// nil preserved: true
}
