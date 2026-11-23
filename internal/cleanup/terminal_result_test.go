package cleanup

import (
	"context"
	"testing"
)

// 通过其他上下文派生的调用不能取得为某次直接分发预留的结果槽。
func TestTerminalResultBelongsOnlyToDirectDispatch(t *testing.T) {
	original := t.Context()
	var result error
	direct := WithTerminalResult(original, &result)
	if ctx, slot := UnwrapTerminalResult(direct); ctx != original || slot != &result {
		t.Fatal("直接分发没有取得原上下文和结果槽")
	}
	derived, cancel := context.WithCancel(direct)
	defer cancel()
	if ctx, slot := UnwrapTerminalResult(derived); ctx != derived || slot != nil {
		t.Fatal("派生上下文取走了外层分发的结果槽")
	}
}
