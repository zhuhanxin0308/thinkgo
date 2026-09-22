package db

import (
	"context"
	"database/sql"
	"errors"
)

var errSQLPreparationCanceled = errors.New("预编译发起请求已取消，等待者重新获取语句")

// cachedSQLStatement 以使用计数分离缓存元数据与数据库 I/O 的生命周期。
// ready 发布后 statement 和 err 不再改变；users、retired 仅在缓存锁内访问。
type cachedSQLStatement struct {
	ready     chan struct{}
	statement *sql.Stmt
	err       error
	canceled  bool
	users     int
	retired   bool
}

// acquireStatement 合并同一 SQL 的预编译，并允许其他 SQL 独立准备和执行。
func (c *SQLConnection) acquireStatement(ctx context.Context, query string) (*cachedSQLStatement, error) {
	for {
		entry, err := c.acquireStatementAttempt(ctx, query)
		if errors.Is(err, errSQLPreparationCanceled) {
			continue
		}
		return entry, err
	}
}

// acquireStatementAttempt 只执行一次合并准备，发起者取消后有效等待者可独立重试。
func (c *SQLConnection) acquireStatementAttempt(ctx context.Context, query string) (*cachedSQLStatement, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.statementMu.Lock()
	if c.statementsClosed {
		c.statementMu.Unlock()
		return nil, ErrDatabaseClosed
	}
	entry := c.statements[query]
	creator := entry == nil
	var evicted *sql.Stmt
	if creator {
		entry = &cachedSQLStatement{ready: make(chan struct{})}
		if c.statements == nil {
			c.statements = make(map[string]*cachedSQLStatement, c.statementCapacity)
		}
		if len(c.statementOrder) >= c.statementCapacity {
			oldest := c.statementOrder[0]
			c.statementOrder = c.statementOrder[1:]
			previous := c.statements[oldest]
			delete(c.statements, oldest)
			previous.retired = true
			if previous.users == 0 {
				evicted = previous.statement
			}
		}
		c.statements[query] = entry
		c.statementOrder = append(c.statementOrder, query)
	}
	entry.users++
	c.statementUsers.Add(1)
	c.statementMu.Unlock()
	c.closeRetiredStatement(evicted)
	if creator {
		statement, err := c.DB.PrepareContext(ctx, query)
		c.statementMu.Lock()
		entry.statement, entry.err = statement, err
		entry.canceled = err != nil && ctx.Err() != nil
		if err != nil && c.statements[query] == entry {
			delete(c.statements, query)
			for index, current := range c.statementOrder {
				if current == query {
					c.statementOrder = append(c.statementOrder[:index], c.statementOrder[index+1:]...)
					break
				}
			}
			entry.retired = true
		}
		close(entry.ready)
		c.statementMu.Unlock()
	}
	select {
	case <-entry.ready:
		if entry.err != nil {
			c.releaseStatement(entry)
			if entry.canceled && ctx.Err() == nil {
				return nil, errSQLPreparationCanceled
			}
			return nil, entry.err
		}
		if err := ctx.Err(); err != nil {
			c.releaseStatement(entry)
			return nil, err
		}
		return entry, nil
	case <-ctx.Done():
		c.releaseStatement(entry)
		return nil, ctx.Err()
	}
}

func (c *SQLConnection) releaseStatement(entry *cachedSQLStatement) {
	c.statementMu.Lock()
	entry.users--
	var retired *sql.Stmt
	if entry.retired && entry.users == 0 {
		retired = entry.statement
	}
	c.statementMu.Unlock()
	c.closeRetiredStatement(retired)
	c.statementUsers.Done()
}

func (c *SQLConnection) closeRetiredStatement(statement *sql.Stmt) {
	if statement == nil {
		return
	}
	if err := statement.Close(); err != nil {
		c.statementMu.Lock()
		c.statementCloseErr = errors.Join(c.statementCloseErr, err)
		c.statementMu.Unlock()
	}
}

// closeStatements 先封闭新租约，再等待在途执行释放；等待时不占用缓存锁。
func (c *SQLConnection) closeStatements() error {
	if c == nil {
		return nil
	}
	c.statementShutdown.Do(c.shutdownStatements)
	c.statementMu.RLock()
	defer c.statementMu.RUnlock()
	return c.statementCloseErr
}

// shutdownStatements 让并发关闭调用共享同一次资源回收和最终错误。
func (c *SQLConnection) shutdownStatements() {
	c.statementMu.Lock()
	c.statementsClosed = true
	var idle []*sql.Stmt
	for _, entry := range c.statements {
		entry.retired = true
		if entry.users == 0 && entry.statement != nil {
			idle = append(idle, entry.statement)
		}
	}
	c.statements = nil
	c.statementOrder = nil
	c.statementMu.Unlock()
	for _, statement := range idle {
		c.closeRetiredStatement(statement)
	}
	c.statementUsers.Wait()
}
