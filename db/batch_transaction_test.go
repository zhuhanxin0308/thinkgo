package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

type batchTransactionBuilder struct {
	builder.Sqlite
	maxBytes int
	batches  int
	onRebind func(int)
}

// 单列每批一行，便于在事务已开启后控制下一条语句的时序。
func (*batchTransactionBuilder) MaxBindParams() int { return 1 }

func (b *batchTransactionBuilder) MaxBatchStatementBytes() int { return b.maxBytes }

func (b *batchTransactionBuilder) Rebind(statement string) string {
	b.batches++
	if b.onRebind != nil {
		b.onRebind(b.batches)
	}
	return b.Sqlite.Rebind(statement)
}

func TestImplicitBatchPreservesRollbackErrors(t *testing.T) {
	for _, mode := range []string{"batch_error", "context_rollback"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rollbackErr := errors.New("驱动回滚失败")
				backend := &blockingRollbackDriver{rollbackErr: rollbackErr}
				dialect := &batchTransactionBuilder{}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				expected := ErrBatchStatementTooLarge
				if mode == "context_rollback" {
					expected = context.Canceled
					backend.rollbackEntered = make(chan struct{})
					backend.rollbackRelease = make(chan struct{})
					dialect.onRebind = func(int) {
						cancel()
						<-backend.rollbackEntered
					}
				} else {
					// 在执行首批 SQL 前就触发明确的业务错误，独立检验它与回滚错误的身份。
					dialect.maxBytes = 1
				}
				name := fmt.Sprintf("thinkgo_batch_rollback_%d", finalizationDriverSequence.Add(1))
				sql.Register(name, backend)
				handle, err := sql.Open(name, "")
				if err != nil {
					t.Fatal(err)
				}
				database := NewDB(NewSQLConnection(handle, dialect))
				t.Cleanup(func() { _ = database.Close() })
				logger := &captureTxErrorLogger{}
				database.SetLogger(logger)
				var affected int64
				var batchErr error
				done := make(chan struct{})
				go func() {
					defer close(done)
					affected, batchErr = database.Table("records").WithContext(ctx).InsertAll([]map[string]interface{}{{"id": 1}, {"id": 2}})
				}()
				if mode == "context_rollback" {
					<-backend.rollbackEntered
					synctest.Wait()
					select {
					case <-done:
						t.Error("驱动回滚尚未完成，批量插入就已返回")
					default:
					}
					close(backend.rollbackRelease)
				}
				<-done
				if affected != 0 || !errors.Is(batchErr, expected) || !errors.Is(batchErr, rollbackErr) || errors.Is(batchErr, ErrTransactionDone) {
					t.Errorf("批量失败未保留两项真实原因：affected=%d err=%v", affected, batchErr)
				}
				synctest.Wait()
				logger.mu.Lock()
				defer logger.mu.Unlock()
				if len(logger.operations) != 1 || logger.operations[0] != "insert_all" {
					t.Errorf("批量错误日志重复或缺失：%v", logger.operations)
				}
			})
		})
	}
}
