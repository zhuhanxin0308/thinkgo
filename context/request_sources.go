package context

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strings"
)

// ErrInvalidQuery 表示查询参数编码非法，不能使用被部分解析的参数。
var ErrInvalidQuery = errors.New("查询参数编码无效")

func (r *Request) queryValues() url.Values {
	values, _ := r.parsedQuery()
	return values
}

func (r *Request) parsedQuery() (url.Values, error) {
	if r == nil {
		return url.Values{}, nil
	}
	r.queryMu.Lock()
	defer r.queryMu.Unlock()
	text := ""
	if r.raw != nil && r.raw.URL != nil {
		text = r.raw.URL.RawQuery
	}
	if !r.querySet || r.queryText != text {
		r.queryCache, r.queryErr = url.ParseQuery(text)
		r.queryText, r.querySet = text, true
	}
	return r.queryCache, r.queryErr
}

func (r *Request) parsedMediaType() (string, error) {
	if r == nil {
		return "application/x-www-form-urlencoded", nil
	}
	text := r.headerValue("Content-Type")
	if text == "" {
		text = "application/x-www-form-urlencoded"
	}
	r.contentTypeMu.Lock()
	defer r.contentTypeMu.Unlock()
	if !r.contentTypeSet || r.contentTypeText != text {
		media, _, err := mime.ParseMediaType(text)
		r.mediaType, r.contentTypeErr = strings.ToLower(media), nil
		if err != nil {
			r.contentTypeErr = fmt.Errorf("%w: %v", ErrInvalidContentType, err)
		}
		r.contentTypeText, r.contentTypeSet = text, true
	}
	return r.mediaType, r.contentTypeErr
}

// parsedInputOverride 按输入与媒体类型缓存参数树，默认 Parse 与参数读取只构造一次。
func (r *Request) parsedInputOverride() (map[string]any, error, bool) {
	return r.inputOverrideState(true)
}

// inputOverrideState 将验证和参数树读取分开，同时在状态锁内保持输入版本一致。
func (r *Request) inputOverrideState(materialize bool) (map[string]any, error, bool) {
	state := r.peekThinkPHPState()
	if state == nil {
		return nil, nil, false
	}
	state.mu.RLock()
	overridden := state.inputSet
	state.mu.RUnlock()
	if !overridden {
		return nil, nil, false
	}
	media, mediaErr := r.parsedMediaType()
	state.mu.Lock()
	defer state.mu.Unlock()
	if mediaErr != nil {
		return nil, mediaErr, true
	}
	r.parseInputOverrideLocked(state, media, materialize)
	return state.inputValues, state.inputErr, true
}

// parseInputOverrideLocked 的调用方必须持有状态锁，保证校验与物化针对同一版本输入。
func (r *Request) parseInputOverrideLocked(state *requestThinkPHPState, media string, materialize bool) {
	if !state.inputParsed || state.inputMedia != media {
		state.inputValues, state.inputErr = nil, nil
		switch {
		case int64(len(state.inputValue)) > r.maxBodyBytes:
			state.inputErr = fmt.Errorf("%w: 上限 %d 字节", ErrRequestBodyTooLarge, r.maxBodyBytes)
		case isJSONMediaType(media):
			if materialize {
				state.inputValues, state.inputErr = decodeStrictJSONObject([]byte(state.inputValue))
			} else {
				state.inputErr = validateStrictJSONDocument([]byte(state.inputValue), true)
			}
			if state.inputErr != nil {
				state.inputErr = fmt.Errorf("%w: %v", ErrInvalidJSONBody, state.inputErr)
			}
		case media == "application/x-www-form-urlencoded":
			state.inputValues, state.inputErr = parseFormInput(state.inputValue)
			if state.inputErr != nil {
				state.inputErr = fmt.Errorf("%w: %v", ErrInvalidFormBody, state.inputErr)
			}
		case media == "multipart/form-data":
			state.inputErr = fmt.Errorf("%w: multipart 输入必须通过原始请求流提供", ErrInvalidFormBody)
		}
		state.inputMedia, state.inputParsed = media, true
	}
	if materialize && state.inputErr == nil && state.inputValues == nil && isJSONMediaType(media) {
		reader := strictJSONReader{body: []byte(state.inputValue), buildValues: true, keysValidated: true}
		value, err := reader.readValue(0)
		if err != nil {
			state.inputErr = fmt.Errorf("%w: %v", ErrInvalidJSONBody, err)
			return
		}
		state.inputValues = value.(map[string]interface{})
	}
}

// jsonInputOverride 在同一状态锁内取得已验证正文快照，防止并发替换输入时复用旧验证结论。
func (r *Request) jsonInputOverride() ([]byte, bool, bool, error) {
	state := r.peekThinkPHPState()
	if state == nil {
		return nil, false, false, nil
	}
	state.mu.RLock()
	overridden := state.inputSet
	tooLarge := int64(len(state.inputValue)) > r.maxBodyBytes
	state.mu.RUnlock()
	if !overridden {
		return nil, false, false, nil
	}
	if tooLarge {
		return nil, false, true, fmt.Errorf("%w: 上限 %d 字节", ErrRequestBodyTooLarge, r.maxBodyBytes)
	}
	media, mediaErr := r.parsedMediaType()
	state.mu.Lock()
	defer state.mu.Unlock()
	if mediaErr != nil {
		return nil, false, true, mediaErr
	}
	r.parseInputOverrideLocked(state, media, false)
	if state.inputErr != nil {
		return nil, false, true, state.inputErr
	}
	return []byte(state.inputValue), isJSONMediaType(media), true, nil
}

// RequestSources 保留来源、键存在性和多值的请求快照。调用方独占快照，可安全修改。
// 数据不经过兼容字符串过滤器，类型绑定和验证应处理原始输入。
type RequestSources struct {
	Query     url.Values
	Form      url.Values
	Header    http.Header
	Cookies   url.Values
	Body      map[string]any
	MediaType string
	HasBody   bool
}

// QueryValues 返回覆盖规则生效后的查询参数，严格报告原始查询字符串的编码错误。
func (r *Request) QueryValues() (url.Values, error) {
	if values, overridden := r.getSnapshot(); overridden {
		return sourceValues(values), nil
	}
	values, err := r.parsedQuery()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidQuery, err)
	}
	return cloneSourceValues(values), nil
}

// Sources 在严格解析后创建统一快照，复用已经校验的 JSON 树，不重新读取或解码请求体。
func (r *Request) Sources() (RequestSources, error) {
	result, err := r.SourcesFor(SourceSelection{Query: true, Form: true, Header: true, Cookies: true, Body: true})
	return result.RequestSources, err
}

// SourcesFor 严格解析全部输入，但只复制所选来源；未选择的正文仍提供根键用于契约检查。
func (r *Request) SourcesFor(selection SourceSelection) (SelectedRequestSources, error) {
	var result SelectedRequestSources
	if r == nil || r.raw == nil {
		return result, errors.New("请求不能为空")
	}
	if err := r.Parse(); err != nil {
		return result, err
	}
	var err error
	result.Query, err = r.selectedQueryValues(selection.Query)
	if err != nil {
		return result, err
	}
	result.MediaType, err = r.parsedMediaType()
	if err != nil {
		return result, err
	}
	state := r.thinkPHPState()
	state.mu.RLock()
	postConfigured := state.postSet
	if selection.Header {
		if state.headerSet {
			result.Header = state.headerValues.Clone()
		} else {
			result.Header = r.raw.Header.Clone()
		}
	}
	if selection.Cookies {
		if state.cookieSet {
			result.Cookies = sourceValues(state.cookieValues)
		} else {
			result.Cookies = make(url.Values)
			for _, cookie := range r.raw.Cookies() {
				result.Cookies.Add(cookie.Name, cookie.Value)
			}
		}
	}
	inputConfigured := state.inputSet
	if inputConfigured {
		result.HasBody = len(state.inputValue) > 0
	}
	state.mu.RUnlock()
	if !inputConfigured {
		result.HasBody = r.hasOriginalBody()
	}
	formMedia := isFormMediaType(result.MediaType)
	copyValues := selection.Body
	if formMedia {
		copyValues = selection.Form
	}
	values, keys, overridden, err := r.selectedPostSnapshot(copyValues)
	if err != nil {
		return result, err
	}
	if overridden {
		if postConfigured {
			result.HasBody = true
		}
		if formMedia {
			if selection.Form {
				result.Form = sourceValues(values)
			} else {
				result.FormKeys = keys
			}
		} else {
			result.Body, result.BodyKeys = values, keys
		}
	} else if isJSONMediaType(result.MediaType) {
		if selection.Body {
			result.Body = cloneRequestMap(r.jsonBody)
		} else {
			result.BodyKeys = requestSourceKeys(r.jsonBody)
		}
	} else if formMedia {
		if selection.Form {
			result.Form = cloneSourceValues(r.raw.PostForm)
		} else {
			result.FormKeys = requestSourceKeys(r.raw.PostForm)
		}
	}
	return result, nil
}

func cloneSourceValues(values url.Values) url.Values {
	result := make(url.Values, len(values))
	for key, items := range values {
		result[key] = append([]string(nil), items...)
	}
	return result
}

// sourceValues 保留重复值和空集合，不将它们拼成一个字符串。
func sourceValues(values map[string]any) url.Values {
	result := make(url.Values, len(values))
	for key, value := range values {
		if value == nil {
			result[key] = nil
			continue
		}
		reflected := reflect.ValueOf(value)
		if reflected.Kind() == reflect.Slice || reflected.Kind() == reflect.Array {
			items := make([]string, reflected.Len())
			for index := range items {
				items[index] = fmt.Sprint(reflected.Index(index).Interface())
			}
			result[key] = items
		} else {
			result[key] = []string{fmt.Sprint(value)}
		}
	}
	return result
}
