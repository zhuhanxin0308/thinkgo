package command

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
)

func TestMakeModelUsesCurrentORMConvention(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	cmd := &MakeModel{}
	cmd.SetApp(&framework.App{
		BasePath: basePath,
	})

	input := &console.Input{
		Args: []string{"UserProfile"},
	}
	output := console.NewOutputWithWriters(io.Discard, io.Discard, false)

	if err := cmd.Execute(input, output); err != nil {
		t.Fatalf("执行模型生成命令失败: %v", err)
	}

	filename := filepath.Join(basePath, "app", "index", "model", "user_profile.go")
	content, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("生成模型文件失败: %v", err)
	}

	text := string(content)
	if !strings.Contains(text, "type UserProfile struct") {
		t.Fatalf("模型模板应包含正确的结构体定义，实际为:\n%s", text)
	}
	if !strings.Contains(text, "*db.Model") {
		t.Fatalf("模型模板应嵌入 *db.Model，实际为:\n%s", text)
	}
	if !strings.Contains(text, `func NewUserProfile(database *db.DB) (*UserProfile, error)`) {
		t.Fatalf("可选模型构造器必须返回初始化错误，实际为:\n%s", text)
	}
	if !strings.Contains(text, `db.NewModelFor(context.Background(), database, m)`) ||
		!strings.Contains(text, `if err != nil`) ||
		strings.Contains(text, `m.Model = model`) {
		t.Fatalf("模型模板必须通过统一入口注入模型并处理错误，实际为:\n%s", text)
	}

	discovery := readGeneratedFile(t, basePath, "app", "index", applicationDiscoveryFilename)
	if !strings.Contains(discovery, `applicationModel.UserProfile`) ||
		!strings.Contains(discovery, `Models: map[string]interface{}{`) ||
		!strings.Contains(discovery, `"UserProfile": &applicationModel.UserProfile{}`) {
		t.Fatalf("新模型必须立即进入自动装配入口，实际为:\n%s", discovery)
	}
}
