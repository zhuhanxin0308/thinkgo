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

// dispatchBeforeEvent gives each callback an isolated deep payload while
// preserving callback order: accepted changes become the next callback's
// input and ultimately the data sent to the database.
func (m *Model) dispatchBeforeEvent(eventType ModelEventType, data map[string]interface{}) (map[string]interface{}, bool) {
	current := cloneDatabaseMap(data)
	for _, callback := range m.eventSnapshot(eventType) {
		payload := cloneDatabaseMap(current)
		if !callback(payload) {
			return nil, false
		}
		current = payload
	}
	return current, true
}

// dispatchAfterEvent gives every callback its own final persisted snapshot;
// mutations are observational and cannot affect other callbacks or results.
func (m *Model) dispatchAfterEvent(eventType ModelEventType, data map[string]interface{}) {
	for _, callback := range m.eventSnapshot(eventType) {
		_ = callback(cloneDatabaseMap(data))
	}
}

func updateEventResultData(result UpdateResult) map[string]interface{} {
	data := cloneDatabaseMap(result.Data)
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
	return data
}

func deleteEventResultData(base map[string]interface{}, result DeleteResult) map[string]interface{} {
	data := cloneDatabaseMap(base)
	if data == nil {
		data = make(map[string]interface{})
	}
	data["deleted"] = result.Deleted
	if result.RelatedDeletedKnown {
		data["related_deleted"] = result.RelatedDeleted
	}
	return data
}
