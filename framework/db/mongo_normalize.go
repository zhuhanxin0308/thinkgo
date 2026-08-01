package db

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func normalizeMongoDocument(document bson.M, primaryKey string) map[string]interface{} {
	return normalizeMongoDocumentWithAliasInLocation(document, primaryKey, primaryKey, time.Local)
}

// normalizeMongoDocumentWithAliasInLocation 递归把 MongoDB 时间值转换到应用时区。
func normalizeMongoDocumentWithAliasInLocation(document bson.M, primaryKey string, modelPrimaryKey string, location *time.Location) map[string]interface{} {
	result := make(map[string]interface{}, len(document))
	for key, value := range document {
		if primaryKey != modelPrimaryKey && key == primaryKey {
			continue
		}
		if isMongoPrimaryKeyPath(key, primaryKey) {
			if objectID, ok := value.(bson.ObjectID); ok {
				result[key] = objectID.Hex()
				continue
			}
		}
		result[key] = normalizeMongoValueInLocation(value, location)
	}
	if primaryKey != "" && modelPrimaryKey != "" && primaryKey != modelPrimaryKey {
		if value, exists := document[primaryKey]; exists {
			if objectID, ok := value.(bson.ObjectID); ok {
				result[modelPrimaryKey] = objectID.Hex()
			} else {
				result[modelPrimaryKey] = normalizeMongoValueInLocation(value, location)
			}
		}
	}
	return result
}

// normalizeMongoDocumentInPlaceWithAliasInLocation 复用游标解码得到的顶层 map，减少大结果集逐行消费时的重复分配。
// document 由当前游标独占，调用方不得在回调结束后继续依赖其原始 BSON 形状。
func normalizeMongoDocumentInPlaceWithAliasInLocation(document bson.M, primaryKey string, modelPrimaryKey string, location *time.Location) map[string]interface{} {
	if document == nil {
		return make(map[string]interface{})
	}
	primaryValue, primaryExists := document[primaryKey]
	for key, value := range document {
		if primaryKey != modelPrimaryKey && key == primaryKey {
			continue
		}
		if isMongoPrimaryKeyPath(key, primaryKey) {
			if objectID, ok := value.(bson.ObjectID); ok {
				document[key] = objectID.Hex()
				continue
			}
		}
		document[key] = normalizeMongoValueInLocation(value, location)
	}
	if primaryKey != "" && modelPrimaryKey != "" && primaryKey != modelPrimaryKey {
		delete(document, primaryKey)
		if primaryExists {
			if objectID, ok := primaryValue.(bson.ObjectID); ok {
				document[modelPrimaryKey] = objectID.Hex()
			} else {
				document[modelPrimaryKey] = normalizeMongoValueInLocation(primaryValue, location)
			}
		}
	}
	return map[string]interface{}(document)
}

func normalizeMongoValue(value interface{}) interface{} {
	return normalizeMongoValueInLocation(value, time.Local)
}

// normalizeMongoValueInLocation 递归处理嵌套文档、数组及 BSON 原生时间。
func normalizeMongoValueInLocation(value interface{}, location *time.Location) interface{} {
	if location == nil {
		location = time.Local
	}
	switch typed := value.(type) {
	case time.Time:
		return typed.In(location)
	case *time.Time:
		if typed == nil {
			return (*time.Time)(nil)
		}
		converted := typed.In(location)
		return &converted
	case bson.DateTime:
		return time.UnixMilli(int64(typed)).In(location)
	case bson.ObjectID:
		return typed
	case bson.Binary:
		return bson.Binary{Subtype: typed.Subtype, Data: append([]byte(nil), typed.Data...)}
	case bson.M:
		return normalizeMongoDocumentWithAliasInLocation(typed, "", "", location)
	case map[string]interface{}:
		return normalizeMongoDocumentWithAliasInLocation(bson.M(typed), "", "", location)
	case bson.D:
		result := make(map[string]interface{}, len(typed))
		for _, element := range typed {
			result[element.Key] = normalizeMongoValueInLocation(element.Value, location)
		}
		return result
	case bson.A:
		result := make([]interface{}, len(typed))
		for index, item := range typed {
			result[index] = normalizeMongoValueInLocation(item, location)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(typed))
		for index, item := range typed {
			result[index] = normalizeMongoValueInLocation(item, location)
		}
		return result
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}
