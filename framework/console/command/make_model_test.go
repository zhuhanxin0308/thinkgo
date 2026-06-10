package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"thinkgo/framework"
	"thinkgo/framework/console"
)

func TestMakeModelUsesCurrentORMConvention(t *testing.T) {
	basePath := t.TempDir()
	cmd := &MakeModel{}
	cmd.SetApp(&framework.App{
		BasePath: basePath,
	})

	input := &console.Input{
		Args: []string{"UserProfile"},
	}
	output := &console.Output{}

	cmd.Execute(input, output)

	filename := filepath.Join(basePath, "app", "model", "user_profile.go")
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
	if !strings.Contains(text, `m.Model = db.NewModelAuto(database, m)`) {
		t.Fatalf("模型模板应默认使用 NewModelAuto，实际为:\n%s", text)
	}
}
