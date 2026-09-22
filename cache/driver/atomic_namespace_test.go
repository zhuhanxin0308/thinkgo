package driver

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redisDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver/redis"
)

type atomicCacheDriver interface {
	Get(key string) (interface{}, bool, error)
	Set(key string, value interface{}, ttl time.Duration) error
	Update(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error
}

// testAtomicUpdateContract 验证并发读改写、删除和回调失败时的共同驱动契约。
func testAtomicUpdateContract(t *testing.T, driver atomicCacheDriver, initial interface{}, number func(interface{}) int) {
	t.Helper()
	if err := driver.Set("counter", initial, 0); err != nil {
		t.Fatalf("准备原子更新值失败: %v", err)
	}
	const workers = 32
	var wait sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			err := driver.Update("counter", time.Minute, func(value interface{}, found bool) (interface{}, bool, error) {
				if !found {
					return nil, false, errors.New("原子更新丢失现有值")
				}
				return number(value) + 1, false, nil
			})
			if err != nil {
				errorsChannel <- err
			}
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("并发原子更新失败: %v", err)
		}
	}
	value, found, err := driver.Get("counter")
	if err != nil || !found || number(value) != workers {
		t.Fatalf("并发原子更新丢失写入: value=%#v found=%t err=%v", value, found, err)
	}

	callbackErr := errors.New("拒绝更新")
	if err = driver.Update("counter", time.Minute, func(interface{}, bool) (interface{}, bool, error) {
		return nil, false, callbackErr
	}); !errors.Is(err, callbackErr) {
		t.Fatalf("回调错误未传播: %v", err)
	}
	if value, found, err = driver.Get("counter"); err != nil || !found || number(value) != workers {
		t.Fatalf("回调失败改写了原值: value=%#v found=%t err=%v", value, found, err)
	}
	if err = driver.Update("counter", 0, func(interface{}, bool) (interface{}, bool, error) {
		return nil, true, nil
	}); err != nil {
		t.Fatalf("原子删除失败: %v", err)
	}
	if _, found, err = driver.Get("counter"); err != nil || found {
		t.Fatalf("原子删除后仍命中: found=%t err=%v", found, err)
	}
}

func TestMemoryAtomicUpdateContract(t *testing.T) {
	testAtomicUpdateContract(t, NewMemory(), 0, func(value interface{}) int {
		return value.(int)
	})
}

// TestFileAtomicUpdateAcrossDriverInstances 验证同一目录的不同驱动实例也不会丢失更新。
func TestFileAtomicUpdateAcrossDriverInstances(t *testing.T) {
	directory := t.TempDir()
	first, err := NewFile(directory)
	if err != nil {
		t.Fatalf("创建首个文件驱动失败: %v", err)
	}
	second, err := NewFile(directory)
	if err != nil {
		t.Fatalf("创建第二个文件驱动失败: %v", err)
	}
	if err = first.Set("counter", 0, 0); err != nil {
		t.Fatalf("准备文件原子更新值失败: %v", err)
	}
	const workers = 24
	var wait sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		driver := first
		if index%2 == 1 {
			driver = second
		}
		wait.Add(1)
		go func(instance *File) {
			defer wait.Done()
			if updateErr := instance.Update("counter", time.Minute, func(value interface{}, found bool) (interface{}, bool, error) {
				if !found {
					return nil, false, errors.New("文件原子更新丢失现有值")
				}
				return int(value.(float64)) + 1, false, nil
			}); updateErr != nil {
				errorsChannel <- updateErr
			}
		}(driver)
	}
	wait.Wait()
	close(errorsChannel)
	for updateErr := range errorsChannel {
		if updateErr != nil {
			t.Fatalf("文件并发原子更新失败: %v", updateErr)
		}
	}
	value, found, err := first.Get("counter")
	if err != nil || !found || int(value.(float64)) != workers {
		t.Fatalf("文件原子更新丢失写入: value=%#v found=%t err=%v", value, found, err)
	}
}

// TestFileAtomicGuardFilesAreBounded 验证高基数 Session/缓存键只使用固定分片 guard，
// 不会为每个键永久遗留一个跨进程锁文件。
func TestFileAtomicGuardFilesAreBounded(t *testing.T) {
	directory := t.TempDir()
	driver, err := NewFile(directory)
	if err != nil {
		t.Fatalf("创建文件驱动失败: %v", err)
	}
	for index := 0; index < fileMutationGuardShards*2; index++ {
		key := "session-" + strconv.Itoa(index)
		if err = driver.Update(key, time.Minute, func(interface{}, bool) (interface{}, bool, error) {
			return "value", false, nil
		}); err != nil {
			t.Fatalf("写入高基数键失败: key=%s err=%v", key, err)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("读取文件缓存目录失败: %v", err)
	}
	guards := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".guard") {
			guards++
			if !strings.HasPrefix(entry.Name(), ".mutation-") {
				t.Fatalf("发现未分片的永久 guard 文件: %s", entry.Name())
			}
		}
	}
	if guards == 0 || guards > fileMutationGuardShards {
		t.Fatalf("guard 分片数量越界: got=%d max=%d", guards, fileMutationGuardShards)
	}
}

func newAtomicRedisDriver(t *testing.T) *redisDriver.Redis {
	t.Helper()
	server := miniredis.RunT(t)
	host, portText, err := net.SplitHostPort(server.Addr())
	if err != nil {
		t.Fatalf("解析 miniredis 地址失败: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("解析 miniredis 端口失败: %v", err)
	}
	driver, err := redisDriver.NewRedis(map[string]interface{}{
		"host": host, "port": port, "prefix": "atomic:", "timeout_ms": 1000,
	})
	if err != nil {
		t.Fatalf("创建 Redis 驱动失败: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	return driver
}

func TestRedisAtomicUpdateContract(t *testing.T) {
	testAtomicUpdateContract(t, newAtomicRedisDriver(t), 0, func(value interface{}) int {
		return int(value.(float64))
	})
}

// TestDriverClearPreservesFenceMetadata 验证普通 Clear 不会重置单调代际，
// 否则清理前启动的旧请求可能在清理后重新获得相同版本号。
func TestDriverClearPreservesFenceMetadata(t *testing.T) {
	tests := []struct {
		name   string
		driver interface {
			Get(string) (interface{}, bool, error)
			Set(string, interface{}, time.Duration) error
			Clear() error
		}
	}{
		{name: "memory", driver: NewMemory()},
		{name: "file", driver: func() *File {
			driver, err := NewFile(t.TempDir())
			if err != nil {
				t.Fatalf("创建文件驱动失败: %v", err)
			}
			return driver
		}()},
		{name: "redis", driver: newAtomicRedisDriver(t)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fenceKey := cacheFenceMetadataPrefix + "scope:sequence"
			if err := test.driver.Set(fenceKey, int64(7), 0); err != nil {
				t.Fatalf("准备 fencing 元数据失败: %v", err)
			}
			if err := test.driver.Set("business", "remove", 0); err != nil {
				t.Fatalf("准备业务缓存失败: %v", err)
			}
			if err := test.driver.Clear(); err != nil {
				t.Fatalf("清空驱动失败: %v", err)
			}
			if value, found, err := test.driver.Get(fenceKey); err != nil || !found || value == nil {
				t.Fatalf("Clear 重置了 fencing 元数据: value=%#v found=%t err=%v", value, found, err)
			}
			if _, found, err := test.driver.Get("business"); err != nil || found {
				t.Fatalf("Clear 未删除业务缓存: found=%t err=%v", found, err)
			}
		})
	}
}

// TestDriverClearPrefixIsolation 验证 Memory、File 与 Redis 都只删除匹配的逻辑前缀。
func TestDriverClearPrefixIsolation(t *testing.T) {
	tests := []struct {
		name   string
		driver interface {
			Get(string) (interface{}, bool, error)
			Set(string, interface{}, time.Duration) error
			ClearPrefix(string) error
		}
	}{
		{name: "memory", driver: NewMemory()},
		{name: "file", driver: func() *File {
			driver, err := NewFile(t.TempDir())
			if err != nil {
				t.Fatalf("创建文件驱动失败: %v", err)
			}
			return driver
		}()},
		{name: "redis", driver: newAtomicRedisDriver(t)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.driver.Set("tenant-a:first", "A1", 0); err != nil {
				t.Fatalf("写入 tenant-a 失败: %v", err)
			}
			if err := test.driver.Set("tenant-b:first", "B1", 0); err != nil {
				t.Fatalf("写入 tenant-b 失败: %v", err)
			}
			if err := test.driver.ClearPrefix("tenant-a:"); err != nil {
				t.Fatalf("按前缀清理失败: %v", err)
			}
			if _, found, err := test.driver.Get("tenant-a:first"); err != nil || found {
				t.Fatalf("tenant-a 清理后仍命中: found=%t err=%v", found, err)
			}
			if value, found, err := test.driver.Get("tenant-b:first"); err != nil || !found || value != "B1" {
				t.Fatalf("前缀清理误删 tenant-b: value=%#v found=%t err=%v", value, found, err)
			}
		})
	}
}

// TestFileClearPrefixPreservesUnscopedLegacyEntry 验证升级前没有原始键字段的文件项
// 不会被猜测归属并误删，调用方会收到明确迁移边界。
func TestFileClearPrefixPreservesUnscopedLegacyEntry(t *testing.T) {
	driver, err := NewFile(t.TempDir())
	if err != nil {
		t.Fatalf("创建文件驱动失败: %v", err)
	}
	encodedValue, err := json.Marshal("legacy")
	if err != nil {
		t.Fatalf("编码旧值失败: %v", err)
	}
	legacyData, err := json.Marshal(storedItem{Value: encodedValue})
	if err != nil {
		t.Fatalf("编码旧格式文件失败: %v", err)
	}
	legacyPath := driver.cacheFilePath("tenant-a:legacy")
	if err = os.WriteFile(legacyPath, legacyData, 0o600); err != nil {
		t.Fatalf("写入旧格式文件失败: %v", err)
	}
	if err = driver.ClearPrefix("tenant-a:"); !errors.Is(err, ErrUnscopedCacheEntry) {
		t.Fatalf("旧格式前缀清理必须返回 ErrUnscopedCacheEntry: %v", err)
	}
	if _, statErr := os.Stat(legacyPath); statErr != nil {
		t.Fatalf("无法归属的旧文件不得删除: %v", statErr)
	}
}
