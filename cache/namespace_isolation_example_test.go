package cache_test

import (
	"errors"
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

func ExampleNewNamespaceDriver_isolation() {
	backend := cacheDriver.NewMemory()
	left, err := cache.NewNamespaceDriver(backend, "left:", "tag:")
	if err != nil {
		panic(err)
	}
	right, err := cache.NewNamespaceDriver(backend, "right:", "tag:")
	if err != nil {
		panic(err)
	}
	manager := cache.NewCache(nil, left)
	defer func() {
		if err := manager.Close(); err != nil {
			panic(err)
		}
	}()
	if err := manager.RegisterStore("right", right); err != nil {
		panic(err)
	}
	other, err := manager.Store("right")
	if err != nil {
		panic(err)
	}
	if err := manager.Set("item", "left value"); err != nil {
		panic(err)
	}
	if err := other.Set("item", "right value"); err != nil {
		panic(err)
	}
	if err := manager.Flush(); err != nil {
		panic(err)
	}
	value, found, err := other.Get("item")
	if err != nil {
		panic(err)
	}
	fmt.Println(value, found)
	// An option cannot silently rename the keys of an already registered driver.
	err = manager.ConfigureStore("right", cache.StoreOptions{Prefix: "ignored:"})
	fmt.Println(errors.Is(err, cache.ErrInvalidCacheStore))
	// Output:
	// right value true
	// true
}
