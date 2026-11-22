package logcoord

import (
	"path/filepath"
	"testing"
	"time"
)

func TestOnlyGeneratedOwnerNamesAreReserved(t *testing.T) {
	for _, leaf := range []string{"2026-11-22.log", "2026-11-22_1.log", "2026-11-22_10.log"} {
		name := filepath.Base(OwnerPath(filepath.Join("logs", leaf)))
		if log, ok := OwnedLog(name); !ok || log != leaf || !IsMetadata(name) {
			t.Errorf("协调文件名不能还原规范日志名: %q %q", name, log)
		}
	}
	for _, leaf := range []string{"2026-11-22_01.log", "2026-11-22_+1.log"} {
		if _, managed := ManagedDate(leaf, time.UTC); !managed {
			t.Errorf("改变了既有日志候选命名规则: %q", leaf)
		}
		if IsMetadata(filepath.Base(OwnerPath(leaf))) {
			t.Errorf("驱动不会生成的侧文件被保留: %q", leaf)
		}
	}
	for _, name := range []string{"personal.log", ".thinkgo-log.owner-personal.log.guard", ".thinkgo-log.owner-2026-13-22.log.guard", ".thinkgo-log.owner-2026-11-22_x.log.guard", ".thinkgo-log.owner-2026-11-22.txt.guard", ".thinkgo-log.owner-x.log.guard", ".thinkgo-log.guard.backup"} {
		if IsMetadata(name) {
			t.Errorf("用户文件不能识别为日志协调文件: %s", name)
		}
	}
	if !IsMetadata(DirectoryGuard) {
		t.Fatal("目录锁文件没有保留")
	}
}
