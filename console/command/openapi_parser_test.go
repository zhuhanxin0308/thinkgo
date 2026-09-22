package command

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/binding"
	"github.com/zhuhanxin0308/thinkgo/framework/openapi"
)

// TestAPICommentDirectivesPreserveProse 验证普通 Markdown、显式覆盖、分组和弃用原因按约定保留。
func TestAPICommentDirectivesPreserveProse(t *testing.T) {
	text := "Show 原标题。\n\n第一段。\n\n- 列表项\n\n@Summary 查询账户\n@Description 第二段。\n@Description 第三段。\n@Tags \"Account Management\",用户\n@ID users.show\n@Deprecated 请使用新版接口"
	got, err := parseAPIHandlerComments(text, "Show")
	if err != nil || got.Summary != "查询账户" || got.OperationID != "users.show" || len(got.Tags) != 2 || got.Tags[0] != "Account Management" || !got.Deprecated {
		t.Fatalf("注解解析错误: %#v %v", got, err)
	}
	for _, text := range []string{"第一段。", "- 列表项", "第二段。", "第三段。", "请使用新版接口"} {
		if !strings.Contains(got.Description, text) {
			t.Fatalf("正文丢失 %q: %s", text, got.Description)
		}
	}
	for _, text := range []string{"@Tags", "@ID", "@Summary", "@Description", "@Tags a,,b", "@Tags \"bad", "@Router /users [GET]", "@ID first\n@ID second"} {
		if _, err := parseAPIHandlerComments(text, "Show"); err == nil {
			t.Fatalf("非法注解未拒绝: %q", text)
		}
	}
	if got := stripCommentName("Showcase 示例", "Show"); got != "Showcase 示例" {
		t.Fatalf("误删标识符前缀: %s", got)
	}
}

// TestCommentCollectorUsesDeclaredOwners 验证泛型方法、分组类型、块注释、尾注释和匿名对象的归属。
func TestCommentCollectorUsesDeclaredOwners(t *testing.T) {
	source := `package api
type (
	// Envelope 通用结果。
	Envelope[T any] struct {
		// Data 业务数据。
		Data T
	}
	Controller[T any] struct{}
)
/* Show 查询资料。

保留整段说明。
*/
func (c *Controller[T]) Show() {}
func init() {}
func init() {}
type _ struct{}
type _ string
// Result 复合结果。
type Result struct {
	A, B int // 共享说明。
	private int // 不公开。
	Nested []*struct {
		// City 城市。
		City string
		More map[string]struct { Zip string /* 邮编。 */ }
	}
	Patch binding.Optional[struct { City string /* 更新城市。 */ }]
}
`
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "api.go", source, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	comments := openapi.SourceComments{Handlers: make(map[string]openapi.HandlerComments), Types: make(binding.Documentation)}
	if err := collectOpenAPIComments(&comments, fileSet, file, "example.com/api"); err != nil {
		t.Fatal(err)
	}
	if got := comments.Handlers["example.com/api.Controller.Show"]; got.Summary != "查询资料。" || !strings.Contains(got.Description, "整段说明") || len(comments.Handlers) != 1 {
		t.Fatalf("方法或块注释解析错误: %#v", comments.Handlers)
	}
	result := comments.Types["example.com/api.Result"]
	if result.Fields["A"] != "共享说明。" || result.Fields["B"] != "共享说明。" || result.Fields["private"] != "" || result.Inline["Nested"].Fields["City"] != "城市。" || result.Inline["Nested"].Inline["More"].Fields["Zip"] != "邮编。" {
		t.Fatalf("结构体说明归属错误: %#v", result)
	}
	if result.Inline["Patch"].Fields["City"] != "更新城市。" {
		t.Fatal("可选匿名请求丢失字段说明")
	}
	if err := collectOpenAPIComments(&comments, fileSet, file, "example.com/api"); err == nil {
		t.Fatal("重复源码声明未拒绝")
	}
	for _, expression := range []string{"(*Generic[A, B])", "*alias.Model", "Generic[A]"} {
		expr, err := parser.ParseExpr(expression)
		if err != nil || commentTypeName(expr) == "" {
			t.Fatalf("接收者类型未识别: %s %v", expression, err)
		}
	}
	if commentTypeName(&ast.FuncType{}) != "" {
		t.Fatal("非法接收者被猜测为类型")
	}
}
