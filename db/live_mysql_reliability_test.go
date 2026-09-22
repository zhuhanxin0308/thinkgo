//go:build integration

package db_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

const (
	liveMySQLSlowSeconds  = 2
	liveMySQLProbeLimit   = time.Second
	liveMySQLPollInterval = 5 * time.Millisecond
)

// TestLiveMySQLColdStatementIsolation 在真实 MySQL 中确认慢冷 SQL 不阻塞无关查询或取消。
func TestLiveMySQLColdStatementIsolation(t *testing.T) {
	database := connectLiveSQL(t, "mysql")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := database.WithConnection(func(connection db.Connection) error {
		sqlConnection, ok := connection.(*db.SQLConnection)
		if !ok {
			return fmt.Errorf("真实 MySQL 未返回 SQLConnection")
		}
		control, err := sqlConnection.DB.Conn(ctx)
		if err != nil {
			return err
		}
		defer control.Close()
		marker := liveSQLTableName()
		query := fmt.Sprintf("SELECT SLEEP(%d) AS %s", liveMySQLSlowSeconds, marker)
		slowResult := make(chan error, 1)
		go func() {
			_, queryErr := database.QueryContext(ctx, query)
			slowResult <- queryErr
		}()
		// 以 MySQL 当前连接状态证明慢查询已经进入服务端，不靠猜测调度时间。
		probeCtx, probeCancel := context.WithTimeout(ctx, liveMySQLProbeLimit)
		defer probeCancel()
		ticker := time.NewTicker(liveMySQLPollInterval)
		defer ticker.Stop()
		for {
			var active int
			err = control.QueryRowContext(probeCtx, "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE ID <> CONNECTION_ID() AND LOCATE(?, INFO) > 0", marker).Scan(&active)
			if err != nil {
				return err
			}
			if active > 0 {
				break
			}
			select {
			case <-probeCtx.Done():
				return fmt.Errorf("未观察到真实慢查询: %w", probeCtx.Err())
			case <-ticker.C:
			}
		}
		fastCtx, fastCancel := context.WithTimeout(ctx, liveMySQLProbeLimit)
		defer fastCancel()
		started := time.Now()
		rows, err := database.QueryContext(fastCtx, "SELECT 1 AS healthy")
		if err != nil || len(rows) != 1 || time.Since(started) >= liveMySQLProbeLimit {
			return fmt.Errorf("真实慢冷 SQL 阻塞了无关查询: rows=%d elapsed=%s err=%v", len(rows), time.Since(started), err)
		}
		canceledCtx, cancelQuery := context.WithCancel(ctx)
		cancelQuery()
		started = time.Now()
		if _, err := database.QueryContext(canceledCtx, query); err == nil || time.Since(started) >= liveMySQLProbeLimit {
			return fmt.Errorf("已取消查询仍然等待慢 SQL: elapsed=%s err=%v", time.Since(started), err)
		}
		if err := <-slowResult; err != nil {
			return fmt.Errorf("真实慢查询未正常完成: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
