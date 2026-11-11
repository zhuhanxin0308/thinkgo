package framework

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// TestRequestTaskShutdownUsesActiveBudgets 验证预算随活跃租约变化，不受最后接纳任务影响。
func TestRequestTaskShutdownUsesActiveBudgets(t *testing.T) {
	for _, operational := range []bool{false, true} {
		for _, releaseShort := range []bool{false, true} {
			synctest.Test(t, func(t *testing.T) {
				const longBudget = time.Second
				const shortBudget = time.Millisecond
				app := &App{}
				longTask, err := app.AcquireRequestTask(2, longBudget)
				if err != nil {
					t.Fatal(err)
				}
				var shortTask *RequestTask
				if operational {
					shortTask, err = app.AcquireOperationalRequestTask(shortBudget)
				} else {
					shortTask, err = app.AcquireRequestTask(2, shortBudget)
				}
				if err != nil {
					t.Fatal(err)
				}
				if releaseShort {
					shortTask.Release()
				}
				closed := make(chan error, 1)
				go func() { closed <- app.Close() }()
				synctest.Wait()
				time.Sleep(longBudget / 2)
				synctest.Wait()
				select {
				case err := <-closed:
					t.Errorf("长任务预算内不得提前结束关闭: %v", err)
				default:
				}
				shortTask.Release()
				longTask.Release()
				synctest.Wait()
				select {
				case err := <-closed:
					if err != nil {
						t.Errorf("任务完成后关闭失败: %v", err)
					}
				default:
				}
				if err := app.Close(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// TestRequestTaskReleasedBudgetDoesNotExtendClose 验证已完成长任务不会延长剩余短任务预算。
func TestRequestTaskReleasedBudgetDoesNotExtendClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const shortBudget = time.Millisecond
		app := &App{}
		shortTask, err := app.AcquireRequestTask(2, shortBudget)
		if err != nil {
			t.Fatal(err)
		}
		longTask, err := app.AcquireRequestTask(2, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		longTask.Release()
		start := time.Now()
		err = app.Close()
		if !errors.Is(err, ErrRequestTasksPending) || time.Since(start) != shortBudget {
			t.Errorf("已释放任务不得支配关闭窗口: elapsed=%v err=%v", time.Since(start), err)
		}
		shortTask.Release()
		if err := app.Close(); err != nil {
			t.Fatal(err)
		}
	})
}
