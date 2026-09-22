package db

import (
	"context"
	"testing"
)

type modelPageBenchmarkConnection struct {
	modelRecordBenchmarkConnection
}

func (*modelPageBenchmarkConnection) Count(context.Context, CountRequest) (int64, error) {
	return 1, nil
}

// BenchmarkModelPagination 使用相同记录、两次查询及响应类型，比较手写组合与统一分页的框架开销。
func BenchmarkModelPagination(b *testing.B) {
	for _, typed := range []bool{false, true} {
		b.Run(map[bool]string{false: "Manual", true: "Typed"}[typed], func(b *testing.B) {
			database := NewDB(&modelPageBenchmarkConnection{})
			b.Cleanup(func() { _ = database.Close() })
			query := NewModel(database, modelRecordBenchmarkTable).Order("id")
			b.ReportAllocs()
			for b.Loop() {
				var page *Paginator[modelRecordBenchmarkDTO]
				var err error
				if typed {
					page, err = Paginate[modelRecordBenchmarkDTO](query, 1, DefaultPageSize)
				} else {
					total, countErr := query.Count()
					if countErr != nil {
						b.Fatal(countErr)
					}
					var rows []modelRecordBenchmarkDTO
					if err = query.Page(1, DefaultPageSize).Select(&rows); err != nil {
						b.Fatal(err)
					}
					page, err = buildPaginator(rows, total, 1, DefaultPageSize)
				}
				if err != nil || len(page.List) != 1 || page.List[0].ID != modelRecordBenchmarkID {
					b.Fatalf("基准分页结果错误: %#v %v", page, err)
				}
			}
		})
	}
}
