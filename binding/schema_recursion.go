package binding

import (
	"encoding"
	"fmt"
	"reflect"
)

// compileRootNode 在整个类型图就绪后校验递归 Schema；仅与文档有关的错误独立保留，
// 运行时请求绑定可以继续使用原有的输入计划。
func compileRootNode(typ reflect.Type, root, output bool) (*node, error) {
	nodes := make(map[planKey]*node)
	plan, err := compileNode(typ, root, output, nodes, 0)
	if err == nil {
		plan.schemaErr = recursiveSchemaCodecError(nodes, output)
	}
	return plan, err
}

func recursiveSchemaCodecError(nodes map[planKey]*node, output bool) error {
	var pending []*node
	for _, plan := range nodes {
		if plan.elementCycle != noElementCycle {
			pending = append(pending, plan)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	// 从所有新支持的元素环逆向遍历真实字段边和元素边。有限的兄弟字段即使有自定义编码也不受影响。
	parents := make(map[*node][]*node)
	for _, parent := range nodes {
		if parent.elem != nil {
			parents[parent.elem] = append(parents[parent.elem], parent)
		}
		for _, child := range parent.fields {
			parents[child.node] = append(parents[child.node], parent)
		}
	}
	seen := make(map[*node]bool)
	textMarshaler := reflect.TypeFor[encoding.TextMarshaler]()
	for len(pending) > 0 {
		last := len(pending) - 1
		plan := pending[last]
		pending = pending[:last]
		if seen[plan] {
			continue
		}
		seen[plan] = true
		if plan.wrappedOptional {
			return fmt.Errorf("%w: 无法从嵌入 Optional 的 %s 推导递归 JSON 契约", ErrDefinition, plan.typ)
		}
		if output && (plan.typ.Implements(textMarshaler) || reflect.PointerTo(plan.typ).Implements(textMarshaler)) {
			return fmt.Errorf("%w: 无法从 %s 的自定义文本编码推导递归响应契约", ErrDefinition, plan.typ)
		}
		pending = append(pending, parents[plan]...)
	}
	return nil
}
