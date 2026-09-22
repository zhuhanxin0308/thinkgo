package asynq

import (
	"errors"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/queue"
)

// TestBackendTaskOwnsItsData 验证适配器只需检查零值，但必须保留后端与不可变任务的数据隔离。
func TestBackendTaskOwnsItsData(t *testing.T) {
	if _, err := backendTask(queue.Task{}); !errors.Is(err, queue.ErrInvalidTask) {
		t.Fatalf("零值任务未拒绝: %v", err)
	}
	task, err := queue.NewTask("mail.send", []byte("payload"), map[string]string{"trace": "original"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := backendTask(task)
	if err != nil {
		t.Fatal(err)
	}
	first.Payload()[0] = 'X'
	first.Headers()["trace"] = "changed"
	second, err := backendTask(task)
	if err != nil {
		t.Fatal(err)
	}
	if string(second.Payload()) != "payload" || second.Headers()["trace"] != "original" || string(task.Payload()) != "payload" {
		t.Fatal("后端修改污染了任务快照")
	}
}
