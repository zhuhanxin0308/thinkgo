package framework

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/cache"
)

const cacheSessionNamespacePrefix = "thinkgo-session:"

// cacheSessionDriver 使用已配置缓存 store 实现 ThinkPHP 的 session.type=cache。
type cacheSessionDriver struct {
	store     *cache.Cache
	expire    time.Duration
	namespace string
}

func newCacheSessionDriver(store *cache.Cache, expire time.Duration, scope string) *cacheSessionDriver {
	return &cacheSessionDriver{store: store, expire: expire, namespace: cacheSessionNamespace(scope)}
}

func cacheSessionNamespace(scope string) string {
	if scope == "" {
		scope = "default"
	}
	digest := sha256.Sum256([]byte(scope))
	return cacheSessionNamespacePrefix + hex.EncodeToString(digest[:]) + ":"
}

// cacheSessionScope 优先使用显式 session.prefix 作为跨主机稳定作用域；
// 未配置时使用绝对项目根目录，默认阻断同一共享缓存中的跨项目碰撞。
func cacheSessionScope(app *App, name, store, prefix string) string {
	projectScope := strings.TrimSpace(prefix)
	if projectScope == "" && app != nil {
		projectScope = filepath.Clean(app.BasePath)
		if runtime.GOOS == "windows" {
			projectScope = strings.ToLower(projectScope)
		}
	}
	parts := []string{projectScope, name, store}
	if app != nil {
		parts = append(parts, app.DisplayName(), app.GetNamespace(), app.CurrentApplicationName())
	}
	return strings.Join(parts, "\x00")
}

func (driver *cacheSessionDriver) namespacePrefix() string {
	if driver != nil && driver.namespace != "" {
		return driver.namespace
	}
	return cacheSessionNamespace("")
}

func (driver *cacheSessionDriver) key(id string) string {
	return driver.namespacePrefix() + id
}

func (driver *cacheSessionDriver) Read(id string) (string, bool, error) {
	return driver.ReadContext(context.Background(), id)
}

func (driver *cacheSessionDriver) ReadContext(ctx context.Context, id string) (string, bool, error) {
	value, found, err := driver.store.GetContext(ctx, driver.key(id))
	if err != nil || !found {
		return "", found, err
	}
	content, ok := value.(string)
	if !ok {
		return "", false, fmt.Errorf("Session 缓存 %q 不是字符串", id)
	}
	return content, true, nil
}

func (driver *cacheSessionDriver) Write(id string, data string) error {
	return driver.store.Update(driver.key(id), driver.expire, func(interface{}, bool) (interface{}, bool, error) {
		return data, false, nil
	})
}

func (driver *cacheSessionDriver) Delete(id string) error {
	return driver.store.Update(driver.key(id), driver.expire, func(interface{}, bool) (interface{}, bool, error) {
		return nil, true, nil
	})
}

func (driver *cacheSessionDriver) Clear() error {
	return driver.store.ClearPrefix(driver.namespacePrefix())
}

func (driver *cacheSessionDriver) Update(id string, update func(string, bool) (string, bool, error)) error {
	return driver.UpdateContext(context.Background(), id, update)
}

func (driver *cacheSessionDriver) UpdateContext(ctx context.Context, id string, update func(string, bool) (string, bool, error)) error {
	if ctx == nil {
		return cache.ErrInvalidCacheContext
	}
	if update == nil {
		return errors.New("Session 缓存更新回调不能为空")
	}
	return driver.store.UpdateContext(ctx, driver.key(id), driver.expire, func(value interface{}, found bool) (interface{}, bool, error) {
		current := ""
		if found {
			var ok bool
			current, ok = value.(string)
			if !ok {
				return nil, false, fmt.Errorf("Session 缓存 %q 不是字符串", id)
			}
		}
		next, remove, err := update(current, found)
		if err != nil {
			return nil, false, err
		}
		return next, remove, nil
	})
}
