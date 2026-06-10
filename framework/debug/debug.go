package debug

import (
	"runtime"
	"sync"
	"time"
)

const defaultMemStatsSampleInterval = 5 * time.Second

var defaultMemSampler = newMemStatsSampler(defaultMemStatsSampleInterval, runtime.ReadMemStats)

// memStatsSampler 负责对运行时内存统计做时间窗口缓存，避免请求路径频繁触发 ReadMemStats。
type memStatsSampler struct {
	lock         sync.Mutex
	interval     time.Duration
	reader       func(*runtime.MemStats)
	now          func() time.Time
	lastSampleAt time.Time
	lastAlloc    uint64
}

// newMemStatsSampler 创建内存采样器。
func newMemStatsSampler(interval time.Duration, reader func(*runtime.MemStats)) *memStatsSampler {
	if interval <= 0 {
		interval = defaultMemStatsSampleInterval
	}
	if reader == nil {
		reader = runtime.ReadMemStats
	}
	return &memStatsSampler{
		interval: interval,
		reader:   reader,
		now:      time.Now,
	}
}

// CurrentAlloc 返回当前采样窗口内的内存使用量。
func (s *memStatsSampler) CurrentAlloc() uint64 {
	if s == nil {
		return 0
	}

	s.lock.Lock()
	defer s.lock.Unlock()

	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	if !s.lastSampleAt.IsZero() && now.Sub(s.lastSampleAt) < s.interval {
		return s.lastAlloc
	}

	var stats runtime.MemStats
	s.reader(&stats)
	s.lastSampleAt = now
	s.lastAlloc = stats.Alloc
	return s.lastAlloc
}

// Debug 调试信息管理器（每个请求应创建独立实例）
// 对应 ThinkPHP 8 的 Trace 调试面板数据收集器
type Debug struct {
	logs       []map[string]interface{} // 日志条目
	sqls       []map[string]interface{} // SQL 查询记录
	cache      []map[string]interface{} // 缓存操作记录
	vars       map[string]interface{}   // 调试变量
	files      []string                 // 加载的文件
	start      time.Time                // 请求开始时间
	lock       sync.RWMutex             // 读写锁
	memSampler *memStatsSampler         // 内存采样器
	Enabled    bool                     // 是否启用调试
}

// NewDebug 创建调试管理器（全局配置级别，保持向后兼容）
func NewDebug() *Debug {
	return &Debug{
		logs:       make([]map[string]interface{}, 0),
		sqls:       make([]map[string]interface{}, 0),
		cache:      make([]map[string]interface{}, 0),
		vars:       make(map[string]interface{}),
		files:      make([]string, 0),
		start:      time.Now(),
		memSampler: defaultMemSampler,
		Enabled:    false,
	}
}

// NewRequestDebug 创建请求级调试实例（并发安全）
// 每个请求独立的 Debug 实例，不与其他请求共享数据
func NewRequestDebug(enabled bool) *Debug {
	return &Debug{
		logs:       make([]map[string]interface{}, 0),
		sqls:       make([]map[string]interface{}, 0),
		cache:      make([]map[string]interface{}, 0),
		vars:       make(map[string]interface{}),
		files:      make([]string, 0),
		start:      time.Now(),
		memSampler: defaultMemSampler,
		Enabled:    enabled,
	}
}

// Clear 清除所有调试数据
func (d *Debug) Clear() {
	d.lock.Lock()
	defer d.lock.Unlock()
	d.logs = make([]map[string]interface{}, 0)
	d.sqls = make([]map[string]interface{}, 0)
	d.cache = make([]map[string]interface{}, 0)
	d.vars = make(map[string]interface{})
	d.files = make([]string, 0)
	d.start = time.Now()
}

// AddLog 添加日志条目
func (d *Debug) AddLog(level, msg string) {
	if !d.Enabled {
		return
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	d.logs = append(d.logs, map[string]interface{}{
		"level": level,
		"msg":   msg,
		"time":  time.Now().Format("15:04:05.000"),
	})
}

// AddSql 添加 SQL 查询记录
func (d *Debug) AddSql(sql string, duration time.Duration) {
	if !d.Enabled {
		return
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	d.sqls = append(d.sqls, map[string]interface{}{
		"sql":      sql,
		"duration": duration.Seconds(),
		"time":     time.Now().Format("15:04:05.000"),
	})
}

// AddCache 添加缓存操作记录
func (d *Debug) AddCache(op, key string) {
	if !d.Enabled {
		return
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	d.cache = append(d.cache, map[string]interface{}{
		"op":   op,
		"key":  key,
		"time": time.Now().Format("15:04:05.000"),
	})
}

// AddVar 添加调试变量
func (d *Debug) AddVar(key string, val interface{}) {
	if !d.Enabled {
		return
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	d.vars[key] = val
}

// AddFile 添加文件加载记录（自动去重）
func (d *Debug) AddFile(file string) {
	if !d.Enabled {
		return
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	for _, f := range d.files {
		if f == file {
			return
		}
	}
	d.files = append(d.files, file)
}

// GetInfo 获取所有调试信息
func (d *Debug) GetInfo() map[string]interface{} {
	d.lock.RLock()
	defer d.lock.RUnlock()

	return map[string]interface{}{
		"logs":  d.logs,
		"sqls":  d.sqls,
		"cache": d.cache,
		"vars":  d.vars,
		"files": d.files,
		"time":  time.Since(d.start).Seconds(),
		"mem":   d.memSampler.CurrentAlloc(),
	}
}
