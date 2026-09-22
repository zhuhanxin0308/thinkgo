package route

import (
	"sort"
	"strings"
)

// routeIndex 是 Freeze 阶段构建的动态路由只读索引。
// 索引只负责缩小候选集合，正则、扩展名和参数边界仍由现有匹配器完成最终校验。
type routeIndex struct {
	methods       map[string]*dynamicMethodIndex
	completeMatch bool
	caseSensitive bool
}

type dynamicMethodIndex struct {
	common *routeTrieNode
	exact  map[string]*routeTrieNode
}

// routeTrieNode 按字面量和参数两类边建立不可变前缀树。
type routeTrieNode struct {
	literals   map[string]*routeTrieNode
	param      *routeTrieNode
	routes     []*Route
	seen       map[*Route]struct{}
	extensions map[string]struct{}
}

func buildRouteIndex(dynamicRoutes map[string][]*Route) *routeIndex {
	index := &routeIndex{methods: make(map[string]*dynamicMethodIndex, len(dynamicRoutes)), completeMatch: true, caseSensitive: true}
	for _, routes := range dynamicRoutes {
		for _, registered := range routes {
			if registered == nil || registered.router == nil {
				continue
			}
			index.completeMatch = registered.router.completeMatch
			index.caseSensitive = registered.router.caseSensitive
			break
		}
		if len(routes) > 0 {
			break
		}
	}
	for method, routes := range dynamicRoutes {
		methodIndex := &dynamicMethodIndex{
			common: newRouteTrieNode(),
			exact:  make(map[string]*routeTrieNode),
		}
		for _, registered := range routes {
			if registered == nil {
				continue
			}
			root := methodIndex.common
			if registered.domain != "" {
				root = methodIndex.exact[registered.domain]
				if root == nil {
					root = newRouteTrieNode()
					methodIndex.exact[registered.domain] = root
				}
			}
			insertDynamicRoute(root, registered, index.caseSensitive)
		}
		methodIndex.common.finalize()
		for _, root := range methodIndex.exact {
			root.finalize()
		}
		index.methods[method] = methodIndex
	}
	return index
}

func newRouteTrieNode() *routeTrieNode {
	return &routeTrieNode{literals: make(map[string]*routeTrieNode)}
}

func insertDynamicRoute(root *routeTrieNode, registered *Route, caseSensitive bool) {
	if root == nil || registered == nil {
		return
	}
	if registered.ext != "" {
		if root.extensions == nil {
			root.extensions = make(map[string]struct{})
		}
		root.extensions[registered.ext] = struct{}{}
	}
	var insert func(*routeTrieNode, int)
	insert = func(node *routeTrieNode, partIndex int) {
		if partIndex == len(registered.pathParts) {
			node.addRoute(registered)
			return
		}
		part := registered.pathParts[partIndex]
		if part.optional {
			insert(node, partIndex+1)
		}
		if part.param != "" {
			if node.param == nil {
				node.param = newRouteTrieNode()
			}
			insert(node.param, partIndex+1)
			return
		}
		literal := part.literal
		if !caseSensitive {
			literal = strings.ToLower(literal)
		}
		child := node.literals[literal]
		if child == nil {
			child = newRouteTrieNode()
			node.literals[literal] = child
		}
		insert(child, partIndex+1)
	}
	insert(root, 0)
}

func (node *routeTrieNode) addRoute(registered *Route) {
	if node == nil || registered == nil {
		return
	}
	if node.seen == nil {
		node.seen = make(map[*Route]struct{})
	}
	if _, exists := node.seen[registered]; exists {
		return
	}
	node.seen[registered] = struct{}{}
	node.routes = append(node.routes, registered)
}

func (node *routeTrieNode) finalize() {
	if node == nil {
		return
	}
	sortRoutes(node.routes)
	node.seen = nil
	for _, child := range node.literals {
		child.finalize()
	}
	if node.param != nil {
		node.param.finalize()
	}
}

func sortRoutes(routes []*Route) {
	sort.SliceStable(routes, func(left, right int) bool {
		leftScore := routeSpecificity(routes[left])
		rightScore := routeSpecificity(routes[right])
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		return routes[left].order < routes[right].order
	})
}

// candidates 返回指定 method、域名分区和路径的候选路由，结果顺序与原线性匹配器一致。
func (index *routeIndex) candidates(method, host string, parts []string, exactDomain bool) []*Route {
	if index == nil {
		return nil
	}
	methodIndex := index.methods[method]
	if methodIndex == nil {
		return nil
	}
	root := methodIndex.common
	if exactDomain {
		root = methodIndex.exact[host]
	}
	if root == nil {
		return nil
	}
	stripped, hasRegisteredExtension := stripIndexedRouteExtension(parts, root.extensions)
	if !index.caseSensitive {
		normalized := make([]string, len(parts))
		for partIndex, part := range parts {
			normalized[partIndex] = strings.ToLower(part)
		}
		parts = normalized
		if hasRegisteredExtension {
			normalizedStripped := make([]string, len(stripped))
			for partIndex, part := range stripped {
				normalizedStripped[partIndex] = strings.ToLower(part)
			}
			stripped = normalizedStripped
		}
	}
	var lists routeCandidateLists
	root.collect(parts, 0, &lists, index.completeMatch)
	if hasRegisteredExtension {
		root.collect(stripped, 0, &lists, index.completeMatch)
	}
	return lists.merged()
}

// stripIndexedRouteExtension 只剥离当前域名分区真实注册过的后缀。
// Trie 同时查询原路径和剥离后路径：前者覆盖参数承载后缀及无后缀请求，
// 后者覆盖最后一个有效段为字面量或可选参数被省略的场景；最终语义仍由 Route 校验。
func stripIndexedRouteExtension(parts []string, extensions map[string]struct{}) ([]string, bool) {
	if len(parts) == 0 || len(extensions) == 0 {
		return nil, false
	}
	lastIndex := len(parts) - 1
	last := parts[lastIndex]
	dotIndex := strings.LastIndexByte(last, '.')
	if dotIndex < 0 {
		return nil, false
	}
	if _, exists := extensions[last[dotIndex+1:]]; !exists {
		return nil, false
	}
	stripped := append([]string(nil), parts...)
	if dotIndex == 0 {
		return stripped[:lastIndex], true
	}
	stripped[lastIndex] = last[:dotIndex]
	return stripped, true
}

func (node *routeTrieNode) collect(parts []string, partIndex int, lists *routeCandidateLists, completeMatch bool) {
	if node == nil || lists == nil {
		return
	}
	if len(node.routes) > 0 && (!completeMatch || partIndex == len(parts)) {
		lists.add(node.routes)
	}
	if partIndex == len(parts) {
		return
	}
	if literal := node.literals[parts[partIndex]]; literal != nil {
		literal.collect(parts, partIndex+1, lists, completeMatch)
	}
	if node.param != nil {
		node.param.collect(parts, partIndex+1, lists, completeMatch)
	}
}

type routeCandidateLists struct {
	inline [4][]*Route
	extra  [][]*Route
	count  int
}

func (lists *routeCandidateLists) add(routes []*Route) {
	if lists == nil || len(routes) == 0 {
		return
	}
	if lists.count < len(lists.inline) {
		lists.inline[lists.count] = routes
	} else {
		lists.extra = append(lists.extra, routes)
	}
	lists.count++
}

func (lists *routeCandidateLists) merged() []*Route {
	if lists == nil || lists.count == 0 {
		return nil
	}
	if lists.count == 1 {
		return lists.inline[0]
	}
	all := make([][]*Route, 0, lists.count)
	inlineCount := lists.count
	if inlineCount > len(lists.inline) {
		inlineCount = len(lists.inline)
	}
	all = append(all, lists.inline[:inlineCount]...)
	all = append(all, lists.extra...)
	positions := make([]int, len(all))
	capacity := 0
	for _, candidates := range all {
		capacity += len(candidates)
	}
	merged := make([]*Route, 0, capacity)
	var previous *Route
	for {
		bestList := -1
		var best *Route
		for listIndex, candidates := range all {
			position := positions[listIndex]
			if position >= len(candidates) {
				continue
			}
			candidate := candidates[position]
			if best == nil || routeComesBefore(candidate, best) {
				best = candidate
				bestList = listIndex
			}
		}
		if bestList < 0 {
			break
		}
		positions[bestList]++
		if best != previous {
			merged = append(merged, best)
			previous = best
		}
	}
	return merged
}

func routeComesBefore(left, right *Route) bool {
	leftScore := routeSpecificity(left)
	rightScore := routeSpecificity(right)
	if leftScore != rightScore {
		return leftScore > rightScore
	}
	return left.order < right.order
}
