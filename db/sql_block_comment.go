package db

import "github.com/zhuhanxin0308/thinkgo/v3/internal/sqllex"

// skipSQLBlockComment 从已确认的 /* 起始位置扫描至完整结束边界。
func skipSQLBlockComment(text string, start int, dialect string) (int, bool) {
	return sqllex.BlockCommentEnd(text, start, dialect)
}
