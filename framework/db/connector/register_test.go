package connector

import (
	"os"
	"testing"
)

// TestMain 为 connector 包测试显式装配内置连接器，生产代码不再依赖 init 副作用。
func TestMain(main *testing.M) {
	RegisterBuiltins()
	os.Exit(main.Run())
}
