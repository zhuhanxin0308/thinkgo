//go:build oracle

package db

import (
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

func TestExpressionCommentBoundaryOracle(t *testing.T) {
	assertExpressionCommentBoundary(t, &builder.Oracle{})
}
