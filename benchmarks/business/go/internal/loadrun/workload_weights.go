package loadrun

// values 固定业务顺序，避免 map 遍历顺序改变同一种子的请求轨迹。
func (weights ScenarioWeights) values() []int {
	return []int{weights.Catalog, weights.Detail, weights.Order, weights.Ticket}
}

// weightGCD 使用欧几里得算法约分权重；默认40/30/20/10仍产生原来的十项循环。
func weightGCD(first, second int) int {
	for second != 0 {
		first, second = second, first%second
	}
	return first
}
