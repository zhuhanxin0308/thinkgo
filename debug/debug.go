package debug

import (
	"runtime"
	"sync"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/context"
)

const (
	defaultMemStatsSampleInterval = 5 * time.Second

	// RequestKey 是请求上下文中存放调试 collector 的统一键。
	RequestKey = "_debug"
	// MaxLogEntries 是单个请求保留的最大日志条数。
	MaxLogEntries = 200
	// MaxSQLEntries 是单个请求保留的最大 SQL 条数。
	MaxSQLEntries = 100
	// MaxCacheEntries 是单个请求保留的最大缓存操作条数。
	MaxCacheEntries = 200
	// MaxVarEntries 是单个请求保留的最大调试变量键数。
	MaxVarEntries = 100
	// MaxFileEntries 是单个请求保留的最大文件记录数。
	MaxFileEntries = 200
)

// Kind 表示一类请求调试数据。
type Kind string

const (
	// KindLog 表示日志记录。
	KindLog Kind = "log"
	// KindSQL 表示 SQL 记录。
	KindSQL Kind = "sql"
	// KindCache 表示缓存操作记录。
	KindCache Kind = "cache"
	// KindVar 表示调试变量。
	KindVar Kind = "var"
	// KindFile 表示文件或模板记录。
	KindFile Kind = "file"
)

type truncationFlags uint8

const (
	truncationLog truncationFlags = 1 << iota
	truncationSQL
	truncationCache
	truncationVar
	truncationFile
)

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
	fileSet    map[string]struct{}      // 文件去重集合
	truncated  truncationFlags          // 各类别截断位图
	start      time.Time                // 请求开始时间
	lock       sync.RWMutex             // 读写锁
	memSampler *memStatsSampler         // 内存采样器
	location   *time.Location           // 调试信息显示时区
	logChannel string                   // Trace 选中的日志通道；空值表示全部通道
	now        func() time.Time         // 可替换的当前时间来源
	Enabled    bool                     // 是否启用调试；发布给并发请求后应按不可变配置读取
}

// NewDebug 创建调试管理器（全局配置级别，保持向后兼容）
func NewDebug() *Debug {
	return newDebug(false)
}

// NewRequestDebug 创建请求级调试实例（并发安全）。
// 每个请求独立的 Debug 实例，不与其他请求共享数据。
func NewRequestDebug(enabled bool) *Debug {
	return newDebug(enabled)
}

func newDebug(enabled bool) *Debug {
	return &Debug{
		logs:       make([]map[string]interface{}, 0),
		sqls:       make([]map[string]interface{}, 0),
		cache:      make([]map[string]interface{}, 0),
		vars:       make(map[string]interface{}),
		files:      make([]string, 0),
		fileSet:    make(map[string]struct{}),
		start:      time.Now().UTC(),
		memSampler: defaultMemSampler,
		location:   time.UTC,
		now:        time.Now,
		Enabled:    enabled,
	}
}

// SetLocation 设置调试面板时间使用的应用时区。
func (d *Debug) SetLocation(location *time.Location) {
	if d == nil {
		return
	}
	if location == nil {
		location = time.UTC
	}
	d.lock.Lock()
	d.location = location
	d.lock.Unlock()
}

// SetLogChannel 设置请求 Trace 要读取的日志通道；空值与 ThinkPHP 一致读取全部通道。
func (d *Debug) SetLogChannel(channel string) {
	if d == nil {
		return
	}
	d.lock.Lock()
	d.logChannel = channel
	d.lock.Unlock()
}

// nowLocked 返回持锁状态下按应用时区转换后的当前时间。
func (d *Debug) nowLocked() time.Time {
	location := d.location
	if location == nil {
		location = time.UTC
	}
	now := d.now
	if now == nil {
		now = time.Now
	}
	return now().In(location)
}

// FromRequest 返回请求私有的调试 collector；请求为空、未挂载或类型错误时返回 nil。
func FromRequest(request *context.Request) *Debug {
	if request == nil {
		return nil
	}
	collector, _ := request.GetData(RequestKey).(*Debug)
	return collector
}

// Clear 清除所有调试数据
func (d *Debug) Clear() {
	if d == nil {
		return
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	clear(d.logs)
	d.logs = d.logs[:0]
	clear(d.sqls)
	d.sqls = d.sqls[:0]
	clear(d.cache)
	d.cache = d.cache[:0]
	if d.vars == nil {
		d.vars = make(map[string]interface{})
	} else {
		clear(d.vars)
	}
	clear(d.files)
	d.files = d.files[:0]
	if d.fileSet == nil {
		d.fileSet = make(map[string]struct{})
	} else {
		clear(d.fileSet)
	}
	d.truncated = 0
	d.start = d.nowLocked()
}

// AddLog 添加日志条目；可选 channel 用于 trace.channel 精确筛选。
func (d *Debug) AddLog(level, msg string, channels ...string) {
	if d == nil || !d.Enabled {
		return
	}
	channel := ""
	if len(channels) > 0 {
		channel = channels[0]
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.logChannel != "" && channel != d.logChannel {
		return
	}
	if len(d.logs) >= MaxLogEntries {
		d.truncated |= truncationLog
		return
	}
	d.logs = append(d.logs, map[string]interface{}{
		"level": level,
		"msg":   msg,
		"time":  d.nowLocked().Format("15:04:05.000"),
	})
}

// AddSql 添加 SQL 查询记录
func (d *Debug) AddSql(sql string, duration time.Duration) {
	if d == nil || !d.Enabled {
		return
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if len(d.sqls) >= MaxSQLEntries {
		d.truncated |= truncationSQL
		return
	}
	d.sqls = append(d.sqls, map[string]interface{}{
		"sql":      sql,
		"duration": duration.Seconds(),
		"time":     d.nowLocked().Format("15:04:05.000"),
	})
}

// AddCache 添加缓存操作记录
func (d *Debug) AddCache(op, key string) {
	if d == nil || !d.Enabled {
		return
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if len(d.cache) >= MaxCacheEntries {
		d.truncated |= truncationCache
		return
	}
	d.cache = append(d.cache, map[string]interface{}{
		"op":   op,
		"key":  key,
		"time": d.nowLocked().Format("15:04:05.000"),
	})
}

// AddVar 添加调试变量
func (d *Debug) AddVar(key string, val interface{}) {
	if d == nil || !d.Enabled {
		return
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.vars == nil {
		d.vars = make(map[string]interface{})
	}
	if _, exists := d.vars[key]; !exists && len(d.vars) >= MaxVarEntries {
		d.truncated |= truncationVar
		return
	}
	d.vars[key] = val
}

// AddFile 添加文件加载记录（自动去重）
func (d *Debug) AddFile(file string) {
	if d == nil || !d.Enabled {
		return
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.fileSet == nil {
		d.fileSet = make(map[string]struct{})
	}
	if _, exists := d.fileSet[file]; exists {
		return
	}
	if len(d.files) >= MaxFileEntries {
		d.truncated |= truncationFile
		return
	}
	d.fileSet[file] = struct{}{}
	d.files = append(d.files, file)
}

// Truncated 报告指定类别是否因达到请求上限而丢弃了后续记录。
func (d *Debug) Truncated(kind Kind) bool {
	if d == nil {
		return false
	}
	flag := truncationFlag(kind)
	if flag == 0 {
		return false
	}
	d.lock.RLock()
	truncated := d.truncated&flag != 0
	d.lock.RUnlock()
	return truncated
}

// GetInfo 获取所有调试信息
func (d *Debug) GetInfo() map[string]interface{} {
	if d == nil {
		return emptyDebugInfo()
	}
	d.lock.RLock()
	info := map[string]interface{}{
		"logs":  cloneDebugEntries(d.logs),
		"sqls":  cloneDebugEntries(d.sqls),
		"cache": cloneDebugEntries(d.cache),
		"vars":  cloneDebugVars(d.vars),
		"files": append([]string(nil), d.files...),
		"time":  time.Since(d.start).Seconds(),
		"truncated": map[string]bool{
			string(KindLog):   d.truncated&truncationLog != 0,
			string(KindSQL):   d.truncated&truncationSQL != 0,
			string(KindCache): d.truncated&truncationCache != 0,
			string(KindVar):   d.truncated&truncationVar != 0,
			string(KindFile):  d.truncated&truncationFile != 0,
		},
	}
	sampler := d.memSampler
	d.lock.RUnlock()
	if sampler != nil {
		info["mem"] = sampler.CurrentAlloc()
	} else {
		info["mem"] = uint64(0)
	}
	return info
}

func truncationFlag(kind Kind) truncationFlags {
	switch kind {
	case KindLog:
		return truncationLog
	case KindSQL:
		return truncationSQL
	case KindCache:
		return truncationCache
	case KindVar:
		return truncationVar
	case KindFile:
		return truncationFile
	default:
		return 0
	}
}

func emptyDebugInfo() map[string]interface{} {
	return map[string]interface{}{
		"logs":  []map[string]interface{}{},
		"sqls":  []map[string]interface{}{},
		"cache": []map[string]interface{}{},
		"vars":  map[string]interface{}{},
		"files": []string{},
		"time":  float64(0),
		"mem":   uint64(0),
		"truncated": map[string]bool{
			string(KindLog):   false,
			string(KindSQL):   false,
			string(KindCache): false,
			string(KindVar):   false,
			string(KindFile):  false,
		},
	}
}

// cloneDebugEntries 复制调试条目列表，避免调用方修改内部切片或 map。
func cloneDebugEntries(entries []map[string]interface{}) []map[string]interface{} {
	cloned := make([]map[string]interface{}, len(entries))
	for index, entry := range entries {
		item := make(map[string]interface{}, len(entry))
		for key, value := range entry {
			item[key] = value
		}
		cloned[index] = item
	}
	return cloned
}

// cloneDebugVars 复制调试变量表，保持 Debug 内部状态只由自身方法维护。
func cloneDebugVars(vars map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(vars))
	for key, value := range vars {
		cloned[key] = value
	}
	return cloned
}
