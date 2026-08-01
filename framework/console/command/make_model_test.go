package command

import (
	"io"
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
		t.Fatalf("模型构造器必须暴露 NewModelAuto 的错误，实际为:\n%s", text)
	}
	if !strings.Contains(text, `model, err := db.NewModelAuto(database, m)`) ||
		!strings.Contains(text, `if err != nil`) ||
		!strings.Contains(text, `m.Model = model`) {
		t.Fatalf("模型模板必须完整处理 NewModelAuto 错误，实际为:\n%s", text)
	}
}
