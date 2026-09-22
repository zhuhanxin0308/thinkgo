package binding

// objectPresence 描述提升字段的实际 JSON 出现规则：匿名指针允许整组缺省，
// 组内任一字段出现时，其各层已存在父节点的非缺省字段必须同时存在。
func (builder *schemaBuilder) objectPresence(plan *node, object map[string]any) {
	required := make([]string, 0)
	groups := make(map[string][]string)
	for _, item := range plan.fields {
		if !ruleRequired(item.rules) && (!builder.output || item.omitEmpty) {
			continue
		}
		if !builder.output || len(item.optionalParents) == 0 {
			required = append(required, item.name)
			continue
		}
		parent := item.optionalParents[len(item.optionalParents)-1]
		groups[parent] = append(groups[parent], item.name)
	}
	if len(required) > 0 {
		object["required"] = required
	}
	if !builder.output {
		return
	}
	dependencies := make(map[string][]string)
	for _, item := range plan.fields {
		for _, parent := range item.optionalParents {
			for _, sibling := range groups[parent] {
				if sibling != item.name {
					dependencies[item.name] = append(dependencies[item.name], sibling)
				}
			}
		}
	}
	if len(dependencies) > 0 {
		object["dependentRequired"] = dependencies
	}
}
