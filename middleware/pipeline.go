package middleware

import (
	stdcontext "context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
)

var (
	// ErrMiddlewareAliasNotFound 表示严格别名入口找不到可执行的中间件。
	ErrMiddlewareAliasNotFound = errors.New("middleware alias not found")
)

// Handler 定义中间件处理函数。
type Handler func(*context.Request, func(*context.Request) *context.Response) *context.Response

// Terminator 定义响应发送后的终结回调。
type Terminator func(*context.Request, *context.Response)

// ContextTerminator 定义能够感知请求收尾取消和截止时间的终结回调。
// 新中间件应优先使用该契约；旧 Terminator 继续保持兼容。
type ContextTerminator func(stdcontext.Context, *context.Request, *context.Response)

// TerminationCallback 是请求实际执行过的单个终结回调快照。
// 字段保持私有，调用方只能通过 Invoke 执行，避免改写已经冻结的回调类型。
type TerminationCallback struct {
	legacy     Terminator
	contextual ContextTerminator
}

// Invoke 使用收尾上下文执行回调；旧回调虽然不能主动感知取消，
// HTTP 内核仍会通过整体监督任务提供有界等待和 panic 隔离。
func (callback TerminationCallback) Invoke(ctx stdcontext.Context, request *context.Request, response *context.Response) {
	if callback.contextual != nil {
		callback.contextual(ctx, request, response)
		return
	}
	if callback.legacy != nil {
		callback.legacy(request, response)
	}
}

const requestTerminatorsKey = "__thinkgo_middleware_terminators__"

type pipelineEntry struct {
	handler    Handler
	terminator Terminator
	name       string // 别名（仅通过 PipeByName 注册时填充），用于优先级排序
}

// Pipeline 实现中间件管道，支持 handle 与 terminate 两个生命周期。
// 同时支持 ThinkPHP 风格的别名注册和优先级排序。
type Pipeline struct {
	lock       sync.RWMutex
	pipes      []pipelineEntry
	aliases    map[string]Handler // 中间件别名映射（对应 ThinkPHP config/middleware.php 的 alias）
	priority   []string           // 中间件优先级排序（对应 ThinkPHP config/middleware.php 的 priority）
	ordered    []pipelineEntry
	orderDirty bool
}

// NewPipeline 创建中间件管道。
func NewPipeline() *Pipeline {
	return &Pipeline{
		pipes:      make([]pipelineEntry, 0),
		aliases:    make(map[string]Handler),
		orderDirty: true,
	}
}

// Alias 注册中间件别名。
// 对应 ThinkPHP 的 config/middleware.php 中的 alias 配置。
// 注册后可通过 PipeByName() 按名称引用。
func (p *Pipeline) Alias(name string, handler Handler) *Pipeline {
	name = strings.TrimSpace(name)
	if name == "" || handler == nil {
		return p
	}
	p.lock.Lock()
	defer p.lock.Unlock()
	p.aliases[name] = handler
	return p
}

// ResolveAlias 根据别名解析中间件处理函数。
// 如果别名不存在，返回 nil。
func (p *Pipeline) ResolveAlias(name string) Handler {
	p.lock.RLock()
	defer p.lock.RUnlock()
	return p.aliases[name]
}

// PipeByName 通过别名注册中间件。
// 如果别名未注册，将被忽略。注册时记录别名，供 SetPriority 排序使用。
func (p *Pipeline) PipeByName(name string) *Pipeline {
	p.lock.Lock()
	defer p.lock.Unlock()
	if handler, ok := p.aliases[name]; ok {
		p.pipes = append(p.pipes, pipelineEntry{handler: handler, name: name})
		p.orderDirty = true
	}
	return p
}

// PipeByNameStrict 通过别名注册中间件，缺失或空处理函数时返回稳定错误。
func (p *Pipeline) PipeByNameStrict(name string) error {
	if p == nil {
		return fmt.Errorf("%w: %q", ErrMiddlewareAliasNotFound, name)
	}
	p.lock.Lock()
	defer p.lock.Unlock()
	handler, ok := p.aliases[name]
	if !ok || handler == nil {
		return fmt.Errorf("%w: %q", ErrMiddlewareAliasNotFound, name)
	}
	p.pipes = append(p.pipes, pipelineEntry{handler: handler, name: name})
	p.orderDirty = true
	return nil
}

// SetPriority 设置中间件执行优先级。
// 列表中的中间件别名将按指定顺序排在最前面执行。
// 对应 ThinkPHP 的 config/middleware.php 中的 priority 配置。
func (p *Pipeline) SetPriority(names []string) *Pipeline {
	p.lock.Lock()
	defer p.lock.Unlock()
	p.priority = append([]string(nil), names...)
	p.orderDirty = true
	return p
}

// Unshift 将中间件插入到管道头部（最先执行）。
// 对应 ThinkPHP 的 unshift() 方法。
func (p *Pipeline) Unshift(handler Handler) *Pipeline {
	if p == nil || handler == nil {
		return p
	}
	p.lock.Lock()
	defer p.lock.Unlock()
	entry := pipelineEntry{handler: handler}
	p.pipes = append([]pipelineEntry{entry}, p.pipes...)
	p.orderDirty = true
	return p
}

// Pipe 注册普通中间件。
func (p *Pipeline) Pipe(handler Handler) *Pipeline {
	if p == nil || handler == nil {
		return p
	}
	p.lock.Lock()
	defer p.lock.Unlock()
	p.pipes = append(p.pipes, pipelineEntry{handler: handler})
	p.orderDirty = true
	return p
}

// PipeLifecycle 注册同时具备 handle 和 terminate 生命周期的中间件。
func (p *Pipeline) PipeLifecycle(handler Handler, terminator Terminator) *Pipeline {
	if p == nil || handler == nil || terminator == nil {
		return p
	}
	p.lock.Lock()
	defer p.lock.Unlock()
	p.pipes = append(p.pipes, pipelineEntry{
		// 统一把 terminate 回调挂到请求上下文里，确保 HTTP 内核可以按真实执行顺序收集。
		handler:    Lifecycle(handler, terminator),
		terminator: terminator,
	})
	p.orderDirty = true
	return p
}

// PipeLifecycleContext 注册具备可取消 terminate 生命周期的中间件。
func (p *Pipeline) PipeLifecycleContext(handler Handler, terminator ContextTerminator) *Pipeline {
	if p == nil || handler == nil || terminator == nil {
		return p
	}
	p.lock.Lock()
	defer p.lock.Unlock()
	p.pipes = append(p.pipes, pipelineEntry{
		handler: LifecycleContext(handler, terminator),
	})
	p.orderDirty = true
	return p
}

// Lifecycle 将具备 terminate 生命周期的中间件包装为普通 Handler，便于路由和分组直接复用。
func Lifecycle(handler Handler, terminator Terminator) Handler {
	if handler == nil || terminator == nil {
		return handler
	}

	return func(request *context.Request, next func(*context.Request) *context.Response) *context.Response {
		appendRequestTerminator(request, terminator)
		return handler(request, next)
	}
}

// LifecycleContext 将可取消终结回调包装为普通 Handler，保持与旧 Lifecycle 相同的执行顺序。
func LifecycleContext(handler Handler, terminator ContextTerminator) Handler {
	if handler == nil || terminator == nil {
		return handler
	}
	return func(request *context.Request, next func(*context.Request) *context.Response) *context.Response {
		appendRequestContextTerminator(request, terminator)
		return handler(request, next)
	}
}

// RequestTerminators 返回当前请求已收集的 terminate 回调副本，避免外部误改内部切片。
func RequestTerminators(request *context.Request) []Terminator {
	callbacks := requestTerminationCallbacks(request)
	if len(callbacks) == 0 {
		return nil
	}
	terminators := make([]Terminator, 0, len(callbacks))
	for _, callback := range callbacks {
		terminators = append(terminators, callback.compatibilityTerminator())
	}
	return terminators
}

// RequestTerminationCallbacks 返回保留旧式与可取消回调类型的防御性副本。
func RequestTerminationCallbacks(request *context.Request) []TerminationCallback {
	callbacks := requestTerminationCallbacks(request)
	if len(callbacks) == 0 {
		return nil
	}
	return append([]TerminationCallback(nil), callbacks...)
}

// requestTerminationCallbacks 返回请求内部的终结回调切片，仅供中间件内核使用。
// 它兼容旧代码直接写入的 []Terminator 请求数据。
func requestTerminationCallbacks(request *context.Request) []TerminationCallback {
	if request == nil {
		return nil
	}
	value := request.GetData(requestTerminatorsKey)
	switch callbacks := value.(type) {
	case []TerminationCallback:
		return callbacks
	case []Terminator:
		converted := make([]TerminationCallback, 0, len(callbacks))
		for _, callback := range callbacks {
			if callback != nil {
				converted = append(converted, TerminationCallback{legacy: callback})
			}
		}
		return converted
	default:
		return nil
	}
}

// requestTerminatorDelta 返回新增终结器的防御性副本，避免公共 API 暴露请求内部切片。
func requestTerminatorDelta(request *context.Request, offset int) []Terminator {
	callbacks := requestTerminationCallbacks(request)
	if offset < 0 || offset >= len(callbacks) {
		return nil
	}
	terminators := make([]Terminator, 0, len(callbacks)-offset)
	for _, callback := range callbacks[offset:] {
		terminators = append(terminators, callback.compatibilityTerminator())
	}
	return terminators
}

// appendRequestTerminator 向请求上下文追加 terminate 回调，保持与中间件 handle 执行顺序一致。
func appendRequestTerminator(request *context.Request, terminator Terminator) {
	if request == nil || terminator == nil {
		return
	}

	callbacks := requestTerminationCallbacks(request)
	callbacks = append(callbacks, TerminationCallback{legacy: terminator})
	request.Set(requestTerminatorsKey, callbacks)
}

func appendRequestContextTerminator(request *context.Request, terminator ContextTerminator) {
	if request == nil || terminator == nil {
		return
	}
	callbacks := requestTerminationCallbacks(request)
	callbacks = append(callbacks, TerminationCallback{contextual: terminator})
	request.Set(requestTerminatorsKey, callbacks)
}

func (callback TerminationCallback) compatibilityTerminator() Terminator {
	if callback.legacy != nil {
		return callback.legacy
	}
	if callback.contextual == nil {
		return nil
	}
	return func(request *context.Request, response *context.Response) {
		ctx := stdcontext.Background()
		if request != nil {
			ctx = request.Context()
		}
		callback.contextual(ctx, request, response)
	}
}

// Then 执行中间件管道，并返回响应。
func (p *Pipeline) Then(request *context.Request, destination func(*context.Request) *context.Response) *context.Response {
	return p.execute(request, destination)
}

// ThenHandlers 直接执行动态中间件切片，适用于控制器等每请求解析的管道，避免重复创建 Pipeline 对象。
func ThenHandlers(request *context.Request, handlers []Handler, destination func(*context.Request) *context.Response) *context.Response {
	if len(handlers) == 0 {
		return destination(request)
	}
	if len(handlers) == 1 {
		return handlers[0](request, destination)
	}
	pipeline := destination
	for index := len(handlers) - 1; index >= 1; index-- {
		handler := handlers[index]
		next := pipeline
		pipeline = func(req *context.Request) *context.Response {
			return handler(req, next)
		}
	}
	return handlers[0](request, pipeline)
}

// orderedPipes 返回按优先级排序后的中间件副本。
// priority 列表中按名称指定的中间件按其先后顺序排到最前执行，
// 其余未指定的中间件保持原有相对顺序（稳定排序）。
func (p *Pipeline) orderedPipes() []pipelineEntry {
	if p == nil {
		return nil
	}
	p.lock.RLock()
	if !p.orderDirty && p.ordered != nil {
		ordered := p.ordered
		p.lock.RUnlock()
		return ordered
	}
	p.lock.RUnlock()

	p.lock.Lock()
	defer p.lock.Unlock()
	if !p.orderDirty && p.ordered != nil {
		return p.ordered
	}
	if len(p.priority) == 0 {
		p.ordered = append([]pipelineEntry(nil), p.pipes...)
		p.orderDirty = false
		return p.ordered
	}

	rank := make(map[string]int, len(p.priority))
	for index, name := range p.priority {
		rank[name] = index
	}

	ordered := make([]pipelineEntry, len(p.pipes))
	copy(ordered, p.pipes)
	sort.SliceStable(ordered, func(i, j int) bool {
		ri, iok := rank[ordered[i].name]
		rj, jok := rank[ordered[j].name]
		if iok && jok {
			return ri < rj
		}
		// 已指定优先级的排在未指定的之前；都未指定则保持原序。
		if iok != jok {
			return iok
		}
		return false
	})
	p.ordered = ordered
	p.orderDirty = false
	return p.ordered
}

// ThenWithTerminators 执行中间件管道，同时返回需要在响应发送后执行的 terminate 回调。
func (p *Pipeline) ThenWithTerminators(request *context.Request, destination func(*context.Request) *context.Response) (*context.Response, []Terminator) {
	existingTerminators := len(requestTerminationCallbacks(request))
	response := p.execute(request, destination)
	return response, requestTerminatorDelta(request, existingTerminators)
}

// execute 只负责执行管道；原始终结回调保存在请求中，兼容副本仅由显式调用方创建。
func (p *Pipeline) execute(request *context.Request, destination func(*context.Request) *context.Response) *context.Response {
	pipeline := destination
	pipes := p.orderedPipes()
	if len(pipes) == 0 {
		return pipeline(request)
	}
	if len(pipes) == 1 {
		// 单个中间件不需要构造反向闭包链，但仍保持 terminate 收集语义。
		return pipes[0].handler(request, pipeline)
	}

	// 最外层直接调用首个中间件，反向链只需为后续节点创建闭包，减少一次热路径分配。
	for index := len(pipes) - 1; index >= 1; index-- {
		entry := pipes[index]
		next := pipeline
		pipeline = func(req *context.Request) *context.Response {
			return entry.handler(req, next)
		}
	}

	return pipes[0].handler(request, pipeline)
}
