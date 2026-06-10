package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Neo4jConnection Neo4j 图数据库连接实现
// 将框架统一查询 API 映射为 Cypher 查询
// table 参数映射为 Neo4j 的 Label（节点标签）
type Neo4jConnection struct {
	Driver neo4j.DriverWithContext
}

// Select 查询节点
// table 作为 Label 使用
func (c *Neo4jConnection) Select(table string, fields string, where []string, args []interface{}, order string, limit int, offset int) ([]map[string]interface{}, error) {
	ctx := context.Background()
	session := c.Driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	// 构建 Cypher 查询
	cypher := fmt.Sprintf("MATCH (n:%s)", table)

	// WHERE 子句
	whereClause, params := c.buildCypherWhere(where, args)
	if whereClause != "" {
		cypher += " WHERE " + whereClause
	}

	// RETURN 子句（字段投影）
	if fields != "" && fields != "*" {
		returnFields := make([]string, 0)
		for _, f := range strings.Split(fields, ",") {
			f = strings.TrimSpace(f)
			returnFields = append(returnFields, fmt.Sprintf("n.%s AS %s", f, f))
		}
		cypher += " RETURN " + strings.Join(returnFields, ", ")
	} else {
		cypher += " RETURN n"
	}

	// ORDER BY
	if order != "" {
		orderParts := make([]string, 0)
		for _, part := range strings.Split(order, ",") {
			part = strings.TrimSpace(part)
			if strings.HasSuffix(strings.ToLower(part), " desc") {
				field := strings.TrimSpace(part[:len(part)-5])
				orderParts = append(orderParts, fmt.Sprintf("n.%s DESC", field))
			} else {
				field := strings.TrimSuffix(strings.TrimSpace(part), " asc")
				field = strings.TrimSuffix(field, " ASC")
				field = strings.TrimSpace(field)
				orderParts = append(orderParts, fmt.Sprintf("n.%s", field))
			}
		}
		cypher += " ORDER BY " + strings.Join(orderParts, ", ")
	}

	if offset > 0 {
		cypher += fmt.Sprintf(" SKIP %d", offset)
	}
	if limit > 0 {
		cypher += fmt.Sprintf(" LIMIT %d", limit)
	}

	result, err := session.Run(ctx, cypher, params)
	if err != nil {
		return nil, err
	}

	var results []map[string]interface{}
	for result.Next(ctx) {
		record := result.Record()
		if fields != "" && fields != "*" {
			// 返回投影字段
			row := make(map[string]interface{})
			for _, key := range record.Keys {
				val, _ := record.Get(key)
				row[key] = val
			}
			results = append(results, row)
		} else {
			// 返回节点属性
			if node, ok := record.Values[0].(neo4j.Node); ok {
				results = append(results, node.Props)
			}
		}
	}

	if err := result.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// Insert 创建节点
func (c *Neo4jConnection) Insert(table string, data map[string]interface{}) (int64, error) {
	ctx := context.Background()
	session := c.Driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)

	cypher := fmt.Sprintf("CREATE (n:%s $props) RETURN elementId(n)", table)
	result, err := session.Run(ctx, cypher, map[string]interface{}{"props": data})
	if err != nil {
		return 0, err
	}

	if result.Next(ctx) {
		// elementId 返回字符串，返回 1 表示成功
		return 1, nil
	}

	return 0, result.Err()
}

// Update 更新节点属性
func (c *Neo4jConnection) Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	ctx := context.Background()
	session := c.Driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)

	cypher := fmt.Sprintf("MATCH (n:%s)", table)

	whereClause, params := c.buildCypherWhere(where, args)
	if whereClause != "" {
		cypher += " WHERE " + whereClause
	}

	// SET 子句
	setParts := make([]string, 0, len(data))
	for k, v := range data {
		paramKey := "set_" + k
		setParts = append(setParts, fmt.Sprintf("n.%s = $%s", k, paramKey))
		params[paramKey] = v
	}
	cypher += " SET " + strings.Join(setParts, ", ")
	cypher += " RETURN count(n)"

	result, err := session.Run(ctx, cypher, params)
	if err != nil {
		return 0, err
	}

	if result.Next(ctx) {
		if count, ok := result.Record().Values[0].(int64); ok {
			return count, nil
		}
	}
	return 0, result.Err()
}

// Delete 删除节点
func (c *Neo4jConnection) Delete(table string, where []string, args []interface{}) (int64, error) {
	ctx := context.Background()
	session := c.Driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)

	cypher := fmt.Sprintf("MATCH (n:%s)", table)

	whereClause, params := c.buildCypherWhere(where, args)
	if whereClause != "" {
		cypher += " WHERE " + whereClause
	}

	// DETACH DELETE 删除节点及其所有关系
	cypher += " DETACH DELETE n RETURN count(n)"

	// Neo4j 不直接返回删除计数，先计数再删除
	countCypher := fmt.Sprintf("MATCH (n:%s)", table)
	if whereClause != "" {
		countCypher += " WHERE " + whereClause
	}
	countCypher += " RETURN count(n)"

	countResult, err := session.Run(ctx, countCypher, params)
	if err != nil {
		return 0, err
	}
	var count int64
	if countResult.Next(ctx) {
		if c, ok := countResult.Record().Values[0].(int64); ok {
			count = c
		}
	}

	// 执行删除
	deleteCypher := fmt.Sprintf("MATCH (n:%s)", table)
	if whereClause != "" {
		deleteCypher += " WHERE " + whereClause
	}
	deleteCypher += " DETACH DELETE n"
	_, err = session.Run(ctx, deleteCypher, params)
	if err != nil {
		return 0, err
	}

	return count, nil
}

// Count 统计节点数
func (c *Neo4jConnection) Count(table string, where []string, args []interface{}) (int64, error) {
	ctx := context.Background()
	session := c.Driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	cypher := fmt.Sprintf("MATCH (n:%s)", table)

	whereClause, params := c.buildCypherWhere(where, args)
	if whereClause != "" {
		cypher += " WHERE " + whereClause
	}

	cypher += " RETURN count(n)"
	result, err := session.Run(ctx, cypher, params)
	if err != nil {
		return 0, err
	}

	if result.Next(ctx) {
		return result.Record().Values[0].(int64), nil
	}
	return 0, result.Err()
}

// Close 关闭连接
func (c *Neo4jConnection) Close() error {
	return c.Driver.Close(context.Background())
}

// buildCypherWhere 将 SQL 风格 where 条件转换为 Cypher WHERE 子句
// 返回 (Cypher WHERE 表达式, 参数 map)
func (c *Neo4jConnection) buildCypherWhere(where []string, args []interface{}) (string, map[string]interface{}) {
	params := make(map[string]interface{})
	if len(where) == 0 {
		return "", params
	}

	clauses := make([]string, 0, len(where))
	argIdx := 0

	for i, cond := range where {
		cond = strings.TrimSpace(cond)
		paramName := fmt.Sprintf("w%d", i)

		// IS NULL / IS NOT NULL
		upperCond := strings.ToUpper(cond)
		if strings.HasSuffix(upperCond, " IS NULL") {
			field := strings.TrimSpace(cond[:len(cond)-8])
			clauses = append(clauses, fmt.Sprintf("n.%s IS NULL", field))
			continue
		}
		if strings.HasSuffix(upperCond, " IS NOT NULL") {
			field := strings.TrimSpace(cond[:len(cond)-12])
			clauses = append(clauses, fmt.Sprintf("n.%s IS NOT NULL", field))
			continue
		}

		// 比较运算符
		parsed := false
		for _, op := range []string{">=", "<=", "!=", ">", "<", "="} {
			if strings.Contains(cond, " "+op+" ") {
				parts := strings.SplitN(cond, " "+op+" ", 2)
				field := strings.TrimSpace(parts[0])
				cypherOp := op
				if op == "!=" {
					cypherOp = "<>"
				}
				if argIdx < len(args) {
					params[paramName] = args[argIdx]
					argIdx++
					clauses = append(clauses, fmt.Sprintf("n.%s %s $%s", field, cypherOp, paramName))
				}
				parsed = true
				break
			}
		}
		if !parsed {
			// 无法解析的条件跳过
			continue
		}
	}

	return strings.Join(clauses, " AND "), params
}
