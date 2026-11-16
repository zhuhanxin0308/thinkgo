package debug

const entryTimeLayout = "15:04:05.000"

type logEntry struct {
	level   string
	message string
	at      string
}

func (entry logEntry) snapshot() map[string]interface{} {
	return map[string]interface{}{"level": entry.level, "msg": entry.message, "time": entry.at}
}

type sqlEntry struct {
	query   string
	seconds float64
	at      string
}

func (entry sqlEntry) snapshot() map[string]interface{} {
	return map[string]interface{}{"sql": entry.query, "duration": entry.seconds, "time": entry.at}
}

type cacheEntry struct {
	operation string
	key       string
	at        string
}

func (entry cacheEntry) snapshot() map[string]interface{} {
	return map[string]interface{}{"op": entry.operation, "key": entry.key, "time": entry.at}
}

// snapshotEntries 只在读取公开快照时创建各条目的 map，避免共享可变数据。
func snapshotEntries[T interface{ snapshot() map[string]interface{} }](entries []T) []map[string]interface{} {
	result := make([]map[string]interface{}, len(entries))
	for index, entry := range entries {
		result[index] = entry.snapshot()
	}
	return result
}
