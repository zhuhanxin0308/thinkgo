package command

import (
	"go/types"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
)

// TestCRUDKeepsEmbeddedAndImportedTypes 验证匿名嵌入、类型别名、外部类型及保留名冲突都生成可编译契约。
func TestCRUDKeepsEmbeddedAndImportedTypes(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	writeDiscoveryFixture(t, basePath, "app/admin/model/account.go", `package model
import (
	"time"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
)
type AccountID int64
type DisplayName = string
type Profile struct { Label string }
type Account struct {
	*db.Model
	*Profile
	UID AccountID
	Name DisplayName
	Joined *time.Time
	Input string
	ResourceKey bool
}
func (*Account) ConfigureModel(model *db.Model) error { model.PrimaryKey("uid"); return nil }
`)
	command := &MakeCRUD{}
	command.SetApp(&framework.App{BasePath: basePath})
	input := &console.Input{Args: []string{"admin@Account"}, Options: map[string]string{
		"key": "UID", "path": "/accounts", "write": "UID,Name,Input,ResourceKey", "read": "UID,Name,Label,Joined",
	}}
	if err := command.Execute(input, generatorTestOutput()); err != nil {
		t.Fatal(err)
	}
	source := readGeneratedFile(t, basePath, "app", "admin", "api", "account.go")
	for _, expected := range []string{"model.AccountID", "model.DisplayName", "InputValue", "ResourceKey1", `"time"`} {
		if !strings.Contains(source, expected) {
			t.Fatalf("生成类型缺少 %q:\n%s", expected, source)
		}
	}
	process := exec.Command("go", "test", "-mod=mod", "./...", "-count=1")
	process.Dir = basePath
	process.Env = append(os.Environ(), "GOWORK=off")
	if output, err := process.CombinedOutput(); err != nil {
		t.Fatalf("复杂字段下游编译失败: %v\n%s", err, output)
	}
}

// TestCRUDRejectsAmbiguousModelMetadata 验证重复映射、值嵌入与不完整模型不会被按名称猜测通过。
func TestCRUDRejectsAmbiguousModelMetadata(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	writeDiscoveryFixture(t, basePath, "app/index/model/invalid.go", `package model
import "github.com/zhuhanxin0308/thinkgo/framework/db"
type Missing struct { ID int64 }
type Value struct { db.Model; ID int64 }
type Named struct { Base *db.Model; ID int64 }
type First struct { *db.Model; ID int64 }
type Second struct { *db.Model; ID int64 }
type Duplicate struct { First; Second }
type DuplicateColumn struct { *db.Model; ID int64 `+"`thinkgo:\"id\"`"+`; Other int64 `+"`thinkgo:\"id\"`"+` }
type UnknownOption struct { *db.Model; ID int64 `+"`thinkgo:\"id,unknown\"`"+` }
type InvalidColumn struct { *db.Model; ID int64 `+"`thinkgo:\"id;drop\"`"+` }
type Recursive struct { *db.Model; *Recursive }
`)
	semantic, err := newApplicationSemanticContextFromModule(basePath)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := semantic.Import("example.com/project/app/index/model")
	if err != nil {
		t.Fatal(err)
	}
	base, err := semantic.Import(frameworkImportPath + "/db")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Missing", "Value", "Named", "Duplicate", "DuplicateColumn", "UnknownOption", "InvalidColumn", "Recursive"} {
		if _, err := collectCRUDFields(pkg.Scope().Lookup(name).Type(), base.Scope().Lookup("Model").Type()); err == nil {
			t.Fatalf("非法模型 %s 未拒绝", name)
		}
	}
}

// TestCRUDRejectsNonJSONAndPrivateFieldTypes 验证不可表达的类型返回错误，而非生成无法引用的源码或发生 panic。
func TestCRUDRejectsNonJSONAndPrivateFieldTypes(t *testing.T) {
	pkg := types.NewPackage("example.com/model", "model")
	private := types.NewNamed(types.NewTypeName(0, pkg, "private", nil), types.Typ[types.String], nil)
	channel := types.NewNamed(types.NewTypeName(0, pkg, "Channel", nil), types.NewChan(types.SendRecv, types.Typ[types.Int]), nil)
	for _, typ := range []types.Type{
		private, channel, types.NewMap(types.NewStruct(nil, nil), types.Typ[types.String]),
		types.NewMap(types.Typ[types.Int], types.Typ[types.String]), types.Typ[types.Complex64],
	} {
		if err := validateCRUDFieldType(typ, make(map[types.Type]bool)); err == nil {
			t.Fatalf("非法字段类型 %s 未拒绝", typ)
		}
	}
	for _, typ := range []types.Type{types.NewPointer(types.Typ[types.String]), types.NewSlice(types.Typ[types.Byte]), types.NewArray(types.Typ[types.Int], 2), types.NewMap(types.Typ[types.String], types.Typ[types.Bool])} {
		if err := validateCRUDFieldType(typ, make(map[types.Type]bool)); err != nil {
			t.Fatalf("合法类型 %s 被拒绝: %v", typ, err)
		}
	}
}
