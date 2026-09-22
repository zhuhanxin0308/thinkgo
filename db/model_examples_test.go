package db

import (
	"context"
	"fmt"
)

type exampleUserOutput struct {
	ID   int64  `thinkgo:"id"`
	Name string `thinkgo:"name"`
}

// exampleUserModel 为可运行示例提供内存测试连接；业务代码使用应用注入的模型。
func exampleUserModel() *Model {
	connection := &modelBusinessConnection{
		rows:  []map[string]any{{"id": int64(1), "name": "Ada"}, {"id": int64(2), "name": "Lin"}},
		count: 2,
	}
	return NewModel(NewDB(connection), "users").AutoTimestamp(false)
}

// ExampleModelQuery_Select 展示结构体列表查询，返回错误而不是返回 map 列表。
func ExampleModelQuery_Select() {
	users := exampleUserModel()
	var rows []exampleUserOutput
	if err := users.WithContext(context.Background()).Field("id,name").Order("id").Select(&rows); err != nil {
		panic(err)
	}
	for _, row := range rows {
		fmt.Println(row.ID, row.Name)
	}
	// Output:
	// 1 Ada
	// 2 Lin
}

// ExampleModelQuery_Find 展示区分查询失败、未找到记录和成功返回。
func ExampleModelQuery_Find() {
	users := exampleUserModel()
	var record exampleUserOutput
	found, err := users.Order("id").Find(&record)
	if err != nil {
		panic(err)
	}
	fmt.Println(found, record.ID, record.Name)
	// Output: true 1 Ada
}

// ExamplePaginate 展示同一个模型查询直接返回类型化分页结果。
func ExamplePaginate() {
	users := exampleUserModel()
	const pageSize = 1
	page, err := Paginate[exampleUserOutput](users.Order("id"), 2, pageSize)
	if err != nil {
		panic(err)
	}
	fmt.Println(page.Total, page.Page, page.HasMore, page.List[0].Name)
	// Output: 2 2 false Lin
}
