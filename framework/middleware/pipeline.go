package middleware

import (
	"sort"

	"thinkgo/framework/context"
)

// Handler 定义中间件处理函数。
type Handler func(*context.Request, func(*context.Request) *context.Response) *context.Response

// Terminator 定义响应发送后的终结回调。
type Terminator func(*context.Request, *context.Response)

const requestTerminatorsKey = "__thinkgo_middleware_terminators__"

type pipelineEntry struct {
	handler    Handler
	terminator Terminator
	name       string // 别名（仅通过 PipeByName 注册时填充），用于优先级排序
}

// Pipeline 实现中间件管道，支持 handle 与 terminate 两个生命周期。
// 同时支持 ThinkPHP 风格的别名注册和优先级排序。
type Pipeline struct {
	pipes    []pipelineEntry
	aliases  map[string]Handler   // 中间件别名映射（对应 ThinkPHP config/middleware.php 的 alias）
	priority []string             // 中间件优先级排序（对应 ThinkPHP config/middleware.php 的 priority）
}

// NewPipeline 创建中间件管道。
func NewPipeline() *Pipeline {
	return &Pipeline{
		pipes:   make([]pipelineEntry, 0),
		aliases: make(map[string]Handler),
	}
}

// Alias 注册中间件别名。
// 对应 ThinkPHP 的 config/middleware.php 中的 alias 配置。
// 注册后可通过 PipeByName() 按名称引用。
func (p *Pipeline) Alias(name string, handler Handler) *Pipeline {
	p.aliases[name] = handler
	return p
}

// ResolveAlias 根据别名解析中间件处理函数。
// 如果别名不存在，返回 nil。
func (p *Pipeline) ResolveAlias(name string) Handler {
	return p.aliases[name]
}

// PipeByName 通过别名注册中间件。
// 如果别名未注册，将被忽略。注册时记录别名，供 SetPriority 排序使用。
func (p *Pipeline) PipeByName(name string) *Pipeline {
	if handler, ok := p.aliases[name]; ok {
		p.pipes = append(p.pipes, pipelineEntry{handler: handler, name: name})
	}
	return p
}

// SetPriority 设置中间件执行优先级。
// 列表中的中间件别名将按指定顺序排在最前面执行。
// 对应 ThinkPHP 的 config/middleware.php 中的 priority 配置。
func (p *Pipeline) SetPriority(names []string) *Pipeline {
	p.priority = names
	return p
}

// Unshift 将中间件插入到管道头部（最先执行）。
// 对应 ThinkPHP 的 unshift() 方法。
func (p *Pipeline) Unshift(handler Handler) *Pipeline {
	entry := pipelineEntry{handler: handler}
	p.pipes = append([]pipelineEntry{entry}, p.pipes...)
	return p
}

// Pipe 注册普通中间件。
func (p *Pipeline) Pipe(handler Handler) *Pipeline {
	p.pipes = append(p.pipes, pipelineEntry{handler: handler})
	return p
}

// PipeLifecycle 注册同时具备 handle 和 terminate 生命周期的中间件。
func (p *Pipeline) PipeLifecycle(handler Handler, terminator Terminator) *Pipeline {
	p.pipes = append(p.pipes, pipelineEntry{
		// 统一把 terminate 回调挂到请求上下文里，确保 HTTP 内核可以按真实执行顺序收集。
		handler:    Lifecycle(handler, terminator),
		terminator: terminator,
	})
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

// RequestTerminators 返回当前请求已收集的 terminate 回调副本，避免外部误改内部切片。
func RequestTerminators(request *context.Request) []Terminator {
	if request == nil {
		return nil
	}

	value := request.GetData(requestTerminatorsKey)
	terminators, ok := value.([]Terminator)
	if !ok || len(terminators) == 0 {
		return nil
	}

	copied := make([]Terminator, len(terminators))
	copy(copied, terminators)
	return copied
}

// appendRequestTerminator 向请求上下文追加 terminate 回调，保持与中间件 handle 执行顺序一致。
func appendRequestTerminator(request *context.Request, terminator Terminator) {
	if request == nil || terminator == nil {
		return
	}

	terminators := RequestTerminators(request)
	terminators = append(terminators, terminator)
	request.Set(requestTerminatorsKey, terminators)
}

// Then 执行中间件管道，并返回响应。
func (p *Pipeline) Then(request *context.Request, destination func(*context.Request) *context.Response) *context.Response {
	response, _ := p.ThenWithTerminators(request, destination)
	return response
}

// orderedPipes 返回按优先级排序后的中间件副本。
// priority 列表中按名称指定的中间件按其先后顺序排到最前执行，
// 其余未指定的中间件保持原有相对顺序（稳定排序）。
func (p *Pipeline) orderedPipes() []pipelineEntry {
	if len(p.priority) == 0 {
		return p.pipes
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
	return ordered
}

// ThenWithTerminators 执行中间件管道，同时返回需要在响应发送后执行的 terminate 回调。
func (p *Pipeline) ThenWithTerminators(request *context.Request, destination func(*context.Request) *context.Response) (*context.Response, []Terminator) {
	pipeline := destination
	terminators := make([]Terminator, 0)

	pipes := p.orderedPipes()

	for _, entry := range pipes {
		if entry.terminator != nil {
			terminators = append(terminators, entry.terminator)
		}
	}

	for index := len(pipes) - 1; index >= 0; index-- {
		entry := pipes[index]
		next := pipeline
		pipeline = func(req *context.Request) *context.Response {
			return entry.handler(req, next)
		}
	}

	return pipeline(request), terminators
}
