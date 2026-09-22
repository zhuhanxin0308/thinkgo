package context

import (
	"fmt"
	"net/url"
)

// SourceSelection 指定需要复制值的来源；零值仅执行严格解析并收集正文根字段名。
// 未选择 Query 仍验证其编码，显式 WithGet 覆盖继续屏蔽原始查询字符串。
type SourceSelection struct {
	Query   bool
	Form    bool
	Header  bool
	Cookies bool
	Body    bool
}

// SelectedRequestSources 保存按需创建的独占快照，不与请求内部数据共享可变值。
// BodyKeys 和 FormKeys 仅在对应来源未被选择时提供，键顺序未定义且可由调用方排序。
type SelectedRequestSources struct {
	RequestSources
	BodyKeys []string
	FormKeys []string
}

func (r *Request) selectedQueryValues(selected bool) (url.Values, error) {
	if selected {
		return r.QueryValues()
	}
	state := r.thinkPHPState()
	state.mu.RLock()
	overridden := state.getSet
	state.mu.RUnlock()
	if overridden {
		return nil, nil
	}
	if _, err := r.parsedQuery(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidQuery, err)
	}
	return nil, nil
}

// selectedPostSnapshot 在状态锁内复制显式覆盖，未选择正文时避免深拷贝仅用于拒绝的字段值。
func (r *Request) selectedPostSnapshot(selected bool) (map[string]any, []string, bool, error) {
	state := r.thinkPHPState()
	state.mu.RLock()
	if state.postSet {
		defer state.mu.RUnlock()
		if selected {
			return cloneRequestMap(state.postValues), nil, true, nil
		}
		return nil, requestSourceKeys(state.postValues), true, nil
	}
	state.mu.RUnlock()
	values, err, overridden := r.parsedInputOverride()
	// 首次 Parse 之后仍可能发生 WithInput，必须传播当前版本的错误，不能发布空值或空键快照。
	if err != nil {
		return nil, nil, overridden, err
	}
	if !overridden {
		return nil, nil, false, nil
	}
	if selected {
		return cloneRequestMap(values), nil, true, nil
	}
	return nil, requestSourceKeys(values), true, nil
}

func requestSourceKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
