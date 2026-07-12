// Package version 提供 ThinkGo 各输出界面共享的单一版本来源。
package version

const (
	// ProductName 是框架产品名称。
	ProductName = "ThinkGo"
	// Number 是当前框架语义版本号。
	Number = "2.0.0"
	// Framework 是异常页和调试条使用的简洁版本标签。
	Framework = ProductName + " " + Number
	// Console 是 version 命令使用的完整版本标签。
	Console = ProductName + " Framework v" + Number
)
