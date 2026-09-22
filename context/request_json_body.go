package context

import "fmt"

func (r *Request) parseJSONBody() {
	r.prepareJSONBody(true)
}

// prepareJSONBody 首次访问只执行一次完整校验；参数读取先发生时直接构树，避免重复遍历。
func (r *Request) prepareJSONBody(materialize bool) {
	if r == nil {
		return
	}
	r.jsonOnce.Do(func() {
		body, err := r.readOriginalBody()
		if err != nil {
			r.setJSONError(err)
			return
		}
		if materialize {
			r.jsonBody, err = decodeStrictJSONObject(body)
		} else {
			err = validateStrictJSONDocument(body, true)
		}
		if err != nil {
			r.setJSONError(fmt.Errorf("%w: %v", ErrInvalidJSONBody, err))
			return
		}
		r.jsonStateMu.Lock()
		r.jsonValidated = true
		r.jsonStateMu.Unlock()
	})
	if materialize {
		r.jsonTreeOnce.Do(func() {
			if r.jsonBody != nil || !r.isJSONValidated() {
				return
			}
			// 正文已完整验证且缓存不可变；只构造值树，不再读取流或重复验证语法。
			reader := strictJSONReader{body: r.bodyCache, buildValues: true, keysValidated: true}
			value, err := reader.readValue(0)
			if err != nil {
				r.setJSONError(fmt.Errorf("%w: %v", ErrInvalidJSONBody, err))
				return
			}
			r.jsonBody = value.(map[string]interface{})
		})
	}
}

func (r *Request) isJSONValidated() bool {
	if r == nil {
		return false
	}
	r.jsonStateMu.RLock()
	validated := r.jsonValidated
	r.jsonStateMu.RUnlock()
	return validated
}
