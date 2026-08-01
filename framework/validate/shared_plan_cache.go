package validate

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	// maxSharedValidationPlans 限制进程级验证计划缓存，避免动态规则耗尽内存。
	maxSharedValidationPlans = 256
	// maxSharedValidationPlanKeyBytes 超过上限的动态规则不进入共享缓存。
	maxSharedValidationPlanKeyBytes = 64 << 10
)

// sharedValidationPlanCache 保存不可变的已编译计划；读取使用 sync.Map，避免热路径争用互斥锁。
type sharedValidationPlanCache struct {
	entries sync.Map
	mu      sync.Mutex
	keys    []string
}

var processValidationPlanCache sharedValidationPlanCache

// sharedValidatorCache 保存只读规则快照，供无自定义配置的控制器便捷验证复用。
type sharedValidatorCache struct {
	entries sync.Map
	mu      sync.Mutex
	keys    []string
}

var processValidatorCache sharedValidatorCache

func (c *sharedValidationPlanCache) load(key string) ([]compiledField, bool) {
	value, ok := c.entries.Load(key)
	if !ok {
		return nil, false
	}
	plan, ok := value.([]compiledField)
	return plan, ok
}

func (c *sharedValidationPlanCache) store(key string, plan []compiledField) {
	if key == "" || len(plan) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries.Load(key); exists {
		return
	}
	if len(c.keys) >= maxSharedValidationPlans {
		evicted := c.keys[0]
		copy(c.keys, c.keys[1:])
		c.keys = c.keys[:len(c.keys)-1]
		c.entries.Delete(evicted)
	}
	c.entries.Store(key, plan)
	c.keys = append(c.keys, key)
}

// clear 仅供测试清理进程级缓存，生产代码不依赖清空行为。
func (c *sharedValidationPlanCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries.Range(func(key, _ interface{}) bool {
		c.entries.Delete(key)
		return true
	})
	c.keys = nil
}

// size 返回当前共享计划数量，供测试验证有界行为。
func (c *sharedValidationPlanCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.keys)
}

func (c *sharedValidatorCache) load(key string) (*Validator, bool) {
	value, ok := c.entries.Load(key)
	if !ok {
		return nil, false
	}
	validator, ok := value.(*Validator)
	return validator, ok
}

func (c *sharedValidatorCache) store(key string, validator *Validator) {
	if key == "" || validator == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries.Load(key); exists {
		return
	}
	if len(c.keys) >= maxSharedValidationPlans {
		evicted := c.keys[0]
		copy(c.keys, c.keys[1:])
		c.keys = c.keys[:len(c.keys)-1]
		c.entries.Delete(evicted)
	}
	c.entries.Store(key, validator)
	c.keys = append(c.keys, key)
}

// clear 仅供测试清理进程级验证器缓存。
func (c *sharedValidatorCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries.Range(func(key, _ interface{}) bool {
		c.entries.Delete(key)
		return true
	})
	c.keys = nil
}

// size 返回当前共享验证器数量，供测试验证有界行为。
func (c *sharedValidatorCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.keys)
}

func sharedValidationPlanKey(rules map[string]string, scenes map[string][]string, scene string) (string, bool) {
	definitions := make([]string, 0, len(rules))
	for definition := range rules {
		definitions = append(definitions, definition)
	}
	sort.Strings(definitions)
	sceneNames := make([]string, 0, len(scenes))
	for name := range scenes {
		sceneNames = append(sceneNames, name)
	}
	sort.Strings(sceneNames)

	var builder strings.Builder
	builder.WriteString("v1;")
	appendSharedPlanKeyPart(&builder, scene)
	for _, definition := range definitions {
		builder.WriteByte('r')
		appendSharedPlanKeyPart(&builder, definition)
		appendSharedPlanKeyPart(&builder, rules[definition])
	}
	for _, name := range sceneNames {
		builder.WriteByte('s')
		appendSharedPlanKeyPart(&builder, name)
		fields := scenes[name]
		builder.WriteString(strconv.Itoa(len(fields)))
		builder.WriteByte(':')
		for _, field := range fields {
			appendSharedPlanKeyPart(&builder, field)
		}
	}
	if builder.Len() > maxSharedValidationPlanKeyBytes {
		return "", false
	}
	return builder.String(), true
}

func appendSharedPlanKeyPart(builder *strings.Builder, value string) {
	builder.WriteString(strconv.Itoa(len(value)))
	builder.WriteByte(':')
	builder.WriteString(value)
	builder.WriteByte(';')
}
