package db

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// requireMongoScalar 拒绝把 map/slice 类型的值当作等值/比较条件，防止 NoSQL 运算符注入。
// 例如从 JSON 请求体传入 {"$ne": null} 这类对象若被直接当作匹配值，会退化为运算符注入。
// 统一查询 API 的条件值语义上只应是标量；切片仅由框架在 IN/NOT IN 内部构造。
func requireMongoScalar(value interface{}) error {
	if value == nil {
		return nil
	}
	switch reflect.TypeOf(value).Kind() {
	case reflect.Map, reflect.Slice, reflect.Array, reflect.Struct:
		// []byte（二进制数据）属于合法标量值，单独放行。
		if _, ok := value.([]byte); ok {
			return nil
		}
		return fmt.Errorf("MongoDB 条件值必须为标量，检测到潜在的运算符注入: %T", value)
	default:
		return nil
	}
}

// MongoConnection MongoDB 连接实现
// 实现 Connection 接口，将框架的统一查询 API 转换为 MongoDB 操作
type MongoConnection struct {
	Client   *mongo.Client
	Database string
}

// collection 获取集合引用
func (c *MongoConnection) collection(name string) *mongo.Collection {
	return c.Client.Database(c.Database).Collection(name)
}

// defaultMongoOpTimeout MongoDB 单次操作的默认超时时长。
const defaultMongoOpTimeout = 10 * time.Second

// ctx 创建带超时的上下文。
func (c *MongoConnection) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), defaultMongoOpTimeout)
}

// Select 查询多条记录
func (c *MongoConnection) Select(table string, fields string, where []string, args []interface{}, order string, limit int, offset int) ([]map[string]interface{}, error) {
	ctx, cancel := c.ctx()
	defer cancel()

	filter, err := c.buildFilter(where, args)
	if err != nil {
		return nil, err
	}
	opts := options.Find()

	// 字段投影
	if fields != "" && fields != "*" {
		projection := bson.M{}
		for _, f := range strings.Split(fields, ",") {
			projection[strings.TrimSpace(f)] = 1
		}
		opts.SetProjection(projection)
	}

	// 排序
	if order != "" {
		sort := bson.D{}
		for _, part := range strings.Split(order, ",") {
			part = strings.TrimSpace(part)
			if strings.HasSuffix(strings.ToLower(part), " desc") {
				field := strings.TrimSuffix(strings.TrimSuffix(part, " desc"), " DESC")
				sort = append(sort, bson.E{Key: strings.TrimSpace(field), Value: -1})
			} else {
				field := strings.TrimSuffix(strings.TrimSuffix(part, " asc"), " ASC")
				sort = append(sort, bson.E{Key: strings.TrimSpace(field), Value: 1})
			}
		}
		opts.SetSort(sort)
	}

	if limit > 0 {
		opts.SetLimit(int64(limit))
	}
	if offset > 0 {
		opts.SetSkip(int64(offset))
	}

	cursor, err := c.collection(table).Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var results []bson.M
	if err = cursor.All(ctx, &results); err != nil {
		return nil, err
	}

	// 转换 bson.M → map[string]interface{}，处理 ObjectID
	maps := make([]map[string]interface{}, len(results))
	for i, doc := range results {
		m := make(map[string]interface{})
		for k, v := range doc {
			if oid, ok := v.(primitive.ObjectID); ok {
				m[k] = oid.Hex() // ObjectID 转为十六进制字符串
			} else {
				m[k] = v
			}
		}
		maps[i] = m
	}
	return maps, nil
}

// Insert 插入记录
func (c *MongoConnection) Insert(table string, data map[string]interface{}) (int64, error) {
	ctx, cancel := c.ctx()
	defer cancel()

	res, err := c.collection(table).InsertOne(ctx, data)
	if err != nil {
		return 0, err
	}
	// MongoDB 返回的 InsertedID 不是 int64，返回 1 表示成功
	_ = res
	return 1, nil
}

// Update 更新记录
func (c *MongoConnection) Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	ctx, cancel := c.ctx()
	defer cancel()

	filter, err := c.buildFilter(where, args)
	if err != nil {
		return 0, err
	}
	update := bson.M{"$set": data}

	res, err := c.collection(table).UpdateMany(ctx, filter, update)
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

// Delete 删除记录
func (c *MongoConnection) Delete(table string, where []string, args []interface{}) (int64, error) {
	ctx, cancel := c.ctx()
	defer cancel()

	filter, err := c.buildFilter(where, args)
	if err != nil {
		return 0, err
	}
	res, err := c.collection(table).DeleteMany(ctx, filter)
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// Count 统计记录数
func (c *MongoConnection) Count(table string, where []string, args []interface{}) (int64, error) {
	ctx, cancel := c.ctx()
	defer cancel()

	filter, err := c.buildFilter(where, args)
	if err != nil {
		return 0, err
	}
	count, err := c.collection(table).CountDocuments(ctx, filter)
	return count, err
}

// Close 关闭连接
func (c *MongoConnection) Close() error {
	return c.Client.Disconnect(context.Background())
}

// buildFilter 将 SQL 风格的 where 条件转换为 MongoDB bson.M 过滤器
// 支持的格式：
//   - "field = ?"           → {field: value}
//   - "field != ?"          → {field: {$ne: value}}
//   - "field > ?"           → {field: {$gt: value}}
//   - "field >= ?"          → {field: {$gte: value}}
//   - "field < ?"           → {field: {$lt: value}}
//   - "field <= ?"          → {field: {$lte: value}}
//   - "field LIKE ?"        → {field: {$regex: pattern}}
//   - "field IN (?, ?, ?)"  → {field: {$in: [values...]}}
//   - "field IS NULL"       → {field: nil}
//   - "field IS NOT NULL"   → {field: {$ne: nil}}
func (c *MongoConnection) buildFilter(where []string, args []interface{}) (bson.M, error) {
	filter := bson.M{}
	if len(where) == 0 {
		return filter, nil
	}

	argIdx := 0
	for _, cond := range where {
		cond = strings.TrimSpace(cond)

		// 永假条件
		if cond == "1 = 0" {
			filter["_impossible_"] = true
			continue
		}

		// IS NULL / IS NOT NULL
		upperCond := strings.ToUpper(cond)
		if strings.HasSuffix(upperCond, " IS NULL") {
			field := strings.TrimSpace(cond[:len(cond)-8])
			filter[field] = nil
			continue
		}
		if strings.HasSuffix(upperCond, " IS NOT NULL") {
			field := strings.TrimSpace(cond[:len(cond)-12])
			filter[field] = bson.M{"$ne": nil}
			continue
		}

		// IN 条件
		if strings.Contains(upperCond, " IN (") {
			parts := strings.SplitN(cond, " IN (", 2)
			if len(parts) == 2 {
				field := strings.TrimSpace(parts[0])
				// 统计占位符数量
				placeholderCount := strings.Count(parts[1], "?")
				if argIdx+placeholderCount > len(args) {
					return nil, fmt.Errorf("MongoDB 条件参数不足，无法解析 %q", cond)
				}
				inValues := make([]interface{}, 0, placeholderCount)
				for i := 0; i < placeholderCount; i++ {
					if err := requireMongoScalar(args[argIdx]); err != nil {
						return nil, err
					}
					inValues = append(inValues, args[argIdx])
					argIdx++
				}
				filter[field] = bson.M{"$in": inValues}
				continue
			}
		}

		// NOT IN 条件
		if strings.Contains(upperCond, " NOT IN (") {
			parts := strings.SplitN(cond, " NOT IN (", 2)
			if len(parts) == 2 {
				field := strings.TrimSpace(parts[0])
				placeholderCount := strings.Count(parts[1], "?")
				if argIdx+placeholderCount > len(args) {
					return nil, fmt.Errorf("MongoDB 条件参数不足，无法解析 %q", cond)
				}
				notInValues := make([]interface{}, 0, placeholderCount)
				for i := 0; i < placeholderCount; i++ {
					if err := requireMongoScalar(args[argIdx]); err != nil {
						return nil, err
					}
					notInValues = append(notInValues, args[argIdx])
					argIdx++
				}
				filter[field] = bson.M{"$nin": notInValues}
				continue
			}
		}

		// LIKE 条件 → 正则
		if strings.Contains(upperCond, " LIKE ") {
			parts := strings.SplitN(upperCond, " LIKE ", 2)
			if len(parts) == 2 {
				field := strings.TrimSpace(cond[:len(cond)-len(parts[1])-6])
				if argIdx >= len(args) {
					return nil, fmt.Errorf("MongoDB LIKE 条件参数不足，无法解析 %q", cond)
				}
				if err := requireMongoScalar(args[argIdx]); err != nil {
					return nil, err
				}
				rawPattern := fmt.Sprintf("%v", args[argIdx])
				argIdx++
				// 先对用户输入做正则转义（防止正则元字符注入与 ReDoS），
				// 再把 SQL LIKE 通配符 %/_ 映射为正则 .*/.，最后整体锚定。
				escaped := regexp.QuoteMeta(rawPattern)
				escaped = strings.ReplaceAll(escaped, "%", ".*")
				escaped = strings.ReplaceAll(escaped, "_", ".")
				filter[field] = bson.M{"$regex": "^" + escaped + "$", "$options": "i"}
				continue
			}
		}

		// BETWEEN 条件
		if strings.Contains(upperCond, " BETWEEN ") {
			parts := strings.SplitN(upperCond, " BETWEEN ", 2)
			if len(parts) == 2 {
				field := strings.TrimSpace(cond[:len(cond)-len(parts[1])-9])
				if argIdx+2 > len(args) {
					return nil, fmt.Errorf("MongoDB BETWEEN 条件参数不足，无法解析 %q", cond)
				}
				if err := requireMongoScalar(args[argIdx]); err != nil {
					return nil, err
				}
				if err := requireMongoScalar(args[argIdx+1]); err != nil {
					return nil, err
				}
				filter[field] = bson.M{"$gte": args[argIdx], "$lte": args[argIdx+1]}
				argIdx += 2
				continue
			}
		}

		// 比较运算符
		parsed := false
		for _, op := range []struct {
			sql   string
			mongo string
		}{
			{">=", "$gte"},
			{"<=", "$lte"},
			{"!=", "$ne"},
			{">", "$gt"},
			{"<", "$lt"},
			{"=", ""},
		} {
			if strings.Contains(cond, " "+op.sql+" ") {
				parts := strings.SplitN(cond, " "+op.sql+" ", 2)
				field := strings.TrimSpace(parts[0])
				if argIdx >= len(args) {
					return nil, fmt.Errorf("MongoDB 条件参数不足，无法解析 %q", cond)
				}
				if err := requireMongoScalar(args[argIdx]); err != nil {
					return nil, err
				}
				if op.mongo == "" {
					filter[field] = args[argIdx]
				} else {
					filter[field] = bson.M{op.mongo: args[argIdx]}
				}
				argIdx++
				parsed = true
				break
			}
		}
		if !parsed {
			// 无法解析的条件必须报错而非静默丢弃，否则可能退化为空 filter，
			// 导致 UpdateMany/DeleteMany 误作用于整个集合，造成数据损坏。
			return nil, fmt.Errorf("MongoDB 无法解析查询条件 %q，请使用受支持的条件形式或改用原生查询", cond)
		}
	}
	return filter, nil
}
