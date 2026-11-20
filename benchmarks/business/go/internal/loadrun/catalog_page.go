package loadrun

import (
	"cmp"
	"slices"
	"sync/atomic"

	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/seed"
)

// 只存每类页尾和行数；超大自定义数据集按需复用固定槽，不按声明的商品数分配内存。
const (
	maxCatalogCacheSlots      = 4096
	maxCatalogPrewarmProducts = 1 << 20
)

type catalogPage struct {
	category    int
	count       int
	lastLocalID int64
	more        bool
}

type catalogPages []atomic.Pointer[catalogPage]

func newCatalogPages(c Config) catalogPages {
	categories := min(c.Categories, c.ProductsPerTenant)
	pages := make(catalogPages, min(categories, maxCatalogCacheSlots))
	if categories <= len(pages) && c.ProductsPerTenant <= maxCatalogPrewarmProducts {
		for category := 1; category <= categories; category++ {
			pages.lookup(c, category)
		}
	}
	return pages
}

func (pages catalogPages) lookup(c Config, category int) *catalogPage {
	if category > c.ProductsPerTenant {
		return &catalogPage{category: category}
	}
	slot := &pages[(category-1)%len(pages)]
	if page := slot.Load(); page != nil && page.category == category {
		return page
	}
	page := firstCatalogPage(c, category)
	slot.Store(&page)
	return &page
}

// firstCatalogPage 对应负载器的零游标及 [0, MaximumPriceCents]。按分类步进，
// 分批保留最小的 limit 个键，临时空间至多两页。租户间只差全局 ID 偏移。
func firstCatalogPage(c Config, category int) catalogPage {
	page := catalogPage{category: category}
	if category > c.ProductsPerTenant || category%c.InactiveProductModulus == 0 && c.Categories%c.InactiveProductModulus == 0 {
		return page
	}
	type key struct{ price, localID int64 }
	compare := func(a, b key) int {
		return cmp.Or(cmp.Compare(a.price, b.price), cmp.Compare(a.localID, b.localID))
	}
	possible := 1 + (c.ProductsPerTenant-category)/c.Categories
	limit := min(c.CatalogPageSize, possible)
	// 页大小可以大于小数据集中商品总数。只分配实际可能用到的空间。
	rows := make([]key, 0, min(possible, limit*2))
	for local := category; ; local += c.Categories {
		price := seed.Price(int64(local), c.DatasetSeed)
		if local%c.InactiveProductModulus != 0 && price <= c.MaximumPriceCents {
			rows = append(rows, key{price, int64(local)})
			if len(rows) > limit {
				page.more = true
			}
			if len(rows) == cap(rows) && page.more {
				slices.SortFunc(rows, compare)
				rows = rows[:limit]
			}
		}
		if c.Categories > c.ProductsPerTenant-local {
			break
		}
	}
	if page.more {
		slices.SortFunc(rows, compare)
		rows = rows[:limit]
		page.lastLocalID = rows[limit-1].localID
	}
	page.count = len(rows)
	return page
}
