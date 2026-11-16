package exception

import (
	"errors"
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/snapshot"
)

// ErrExceptionDataSnapshot 表示异常附加数据无法形成隔离的快照。
var ErrExceptionDataSnapshot = errors.New("异常附加数据无法安全复制")

func snapshotExceptionData(data map[string]interface{}, priorErr error) (map[string]interface{}, error) {
	if priorErr != nil || data == nil {
		return nil, priorErr
	}
	value, err := snapshot.Copy(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrExceptionDataSnapshot, err)
	}
	return value.(map[string]interface{}), nil
}
