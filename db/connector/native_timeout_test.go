package connector

import (
	"errors"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

// TestTakeNativeOperationTimeout 验证时长校验及连接驱动选项的隔离。
func TestTakeNativeOperationTimeout(t *testing.T) {
	for _, params := range []map[string]string{
		{"operation_timeout": "52"},
		{"operation_timeout": "-1s"},
		{"operation_timeout": "0s"},
		{"operation_timeout": "1s", "OPERATION_TIMEOUT": "2s"},
	} {
		if _, _, err := takeNativeOperationTimeout(db.Config{Params: params}); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
			t.Errorf("无效时长应在启动时被拒绝: %v", err)
		}
	}
	config := db.Config{Params: map[string]string{"OPERATION_TIMEOUT": " 52s ", "scheme": "bolt+s"}}
	driverConfig, timeout, err := takeNativeOperationTimeout(config)
	if err != nil || timeout != 52*time.Second || len(driverConfig.Params) != 1 || driverConfig.Params["scheme"] != "bolt+s" ||
		config.Params["OPERATION_TIMEOUT"] != " 52s " {
		t.Fatalf("操作预算应与驱动参数隔离: %v %v %v", driverConfig.Params, timeout, err)
	}
	if _, timeout, err := takeNativeOperationTimeout(db.Config{}); err != nil || timeout != defaultNativeOperationTimeout {
		t.Fatalf("省略配置应沿用默认操作预算: %v %v", timeout, err)
	}
}
