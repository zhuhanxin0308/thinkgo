package log

const (
	// maxLogContextNodes 限制单条日志展开后的节点数，防止共享引用图指数级复制。
	maxLogContextNodes = 4096
	// maxLogContextDepth 限制反射访问深度，同时覆盖指针与接口包装。
	maxLogContextDepth = 64
	// maxLogContextBytes 为上下文 JSON 预留最多 1 MiB 的保守编码预算。
	maxLogContextBytes = 1 << 20
	// logJSONNodeBudget 覆盖容器分隔符、数值及 time.Time 的最坏常规 JSON 长度。
	logJSONNodeBudget = 64
	// logJSONEscapeExpansion 一个输入字节最多转义为六字节的 JSON Unicode 序列。
	logJSONEscapeExpansion  = 6
	logTruncatedPlaceholder = "[TRUNCATED]"
)

// logContextBudget 在一次完整脱敏过程中共享，失败后立即终止所有后续分支。
type logContextBudget struct {
	nodes     int
	bytes     int
	exhausted bool
}

func (budget *logContextBudget) visit(depth int) bool {
	if budget.exhausted || depth > maxLogContextDepth || budget.nodes == 0 || budget.bytes < logJSONNodeBudget {
		budget.exhausted = true
		return false
	}
	budget.nodes--
	budget.bytes -= logJSONNodeBudget
	return true
}

func (budget *logContextBudget) text(value string) bool {
	// 先除后乘，超大字符串也不会使整数预算溢出。
	if len(value) > budget.bytes/logJSONEscapeExpansion {
		budget.exhausted = true
		return false
	}
	budget.bytes -= len(value) * logJSONEscapeExpansion
	return true
}

func (budget *logContextBudget) collection(length int) bool {
	if length > budget.nodes {
		budget.exhausted = true
		return false
	}
	return true
}
