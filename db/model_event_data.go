package db

// eventSnapshot copies the callback slice under the model lock so event
// execution never holds framework state locks while calling user code.
func (m *Model) eventSnapshot(eventType ModelEventType) []ModelEventCallback {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	callbacks := append([]ModelEventCallback(nil), m.events[eventType]...)
	m.mu.RUnlock()
	return callbacks
}

// dispatchBeforeEvent 隔离回调载荷，按顺序传递修改，并在后续回调和驱动执行前拒绝非法图。
func (m *Model) dispatchBeforeEvent(eventType ModelEventType, data map[string]interface{}) (map[string]interface{}, bool, error) {
	current, err := cloneDatabaseMap(data)
	if err != nil {
		return nil, false, err
	}
	for _, callback := range m.eventSnapshot(eventType) {
		payload, err := cloneDatabaseMap(current)
		if err != nil {
			return nil, false, err
		}
		if !callback(payload) {
			return nil, false, nil
		}
		current = payload
	}
	current, err = cloneDatabaseMap(current)
	return current, err == nil, err
}

// dispatchAfterEvent gives every callback its own final persisted snapshot;
// mutations are observational and cannot affect other callbacks or results.
func (m *Model) dispatchAfterEvent(eventType ModelEventType, data map[string]interface{}) error {
	for _, callback := range m.eventSnapshot(eventType) {
		payload, err := cloneDatabaseMap(data)
		if err != nil {
			return err
		}
		_ = callback(payload)
	}
	return nil
}

func updateEventResultData(result UpdateResult) (map[string]interface{}, error) {
	data, err := cloneDatabaseMap(result.Data)
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = make(map[string]interface{})
	}
	data["affected"] = result.Count()
	if result.MatchedKnown {
		data["matched"] = result.Matched
	}
	if result.ModifiedKnown {
		data["modified"] = result.Modified
	}
	return data, nil
}

func deleteEventResultData(base map[string]interface{}, result DeleteResult) (map[string]interface{}, error) {
	data, err := cloneDatabaseMap(base)
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = make(map[string]interface{})
	}
	data["deleted"] = result.Deleted
	if result.RelatedDeletedKnown {
		data["related_deleted"] = result.RelatedDeleted
	}
	return data, nil
}
