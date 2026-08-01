package db

import (
	"errors"

	"thinkgo/framework/db/internal/contract"
)

var (
	// ErrInvalidConnector 表示连接器名称、实例或注册参数非法。
	ErrInvalidConnector = errors.New("数据库连接器非法")
	// ErrDuplicateConnector 表示同名连接器已经注册，禁止静默覆盖。
	ErrDuplicateConnector = errors.New("数据库连接器重复")
	// ErrConnectorNotFound 表示请求的连接器未注册。
	ErrConnectorNotFound = errors.New("数据库连接器不存在")
	// ErrInvalidDatabaseConfig 表示数据库配置字段或范围非法。
	ErrInvalidDatabaseConfig = errors.New("数据库配置非法")
	// ErrDatabaseUnavailable 表示 DB 没有可用的底层连接。
	ErrDatabaseUnavailable = errors.New("数据库连接不可用")
	// ErrDatabaseClosed 表示 DB 已关闭，不再接受新操作。
	ErrDatabaseClosed = errors.New("数据库已关闭")
	// ErrInvalidDatabaseContext 表示数据库关闭或操作上下文为空。
	ErrInvalidDatabaseContext = errors.New("数据库上下文无效")
	// ErrInvalidConnectionName 表示 Manager 连接名称非法。
	ErrInvalidConnectionName = errors.New("数据库连接名称非法")
	// ErrDuplicateConnection 表示 Manager 中同名连接已存在。
	ErrDuplicateConnection = errors.New("数据库连接名称重复")
	// ErrConnectionNotFound 表示 Manager 中不存在指定连接。
	ErrConnectionNotFound = errors.New("数据库连接不存在")
	// ErrDatabaseManagerClosed 表示连接管理器已经关闭。
	ErrDatabaseManagerClosed = errors.New("数据库连接管理器已关闭")
	// ErrTimestampOverflow 表示 Unix 时间戳无法安全写入字段当前整数类型。
	ErrTimestampOverflow = errors.New("自动时间戳整数溢出")
	// ErrInvalidQuery 表示查询器依赖、状态或参数非法。
	ErrInvalidQuery = errors.New("数据库查询非法")
	// ErrQueryArgumentsTooMany 表示查询绑定参数超过当前方言或框架预算。
	ErrQueryArgumentsTooMany = errors.New("数据库查询绑定参数过多")
	// ErrBatchStatementTooLarge 表示单行批量数据已经超过当前连接可接受的语句大小预算。
	ErrBatchStatementTooLarge = errors.New("数据库批量语句过大")
	// ErrUnsafeFullTableMutation 表示更新或删除缺少业务 WHERE 条件。
	ErrUnsafeFullTableMutation = errors.New("禁止无条件全表修改")
	// ErrInvalidPagination 表示分页值为负、越界或发生整数溢出。
	ErrInvalidPagination = errors.New("数据库分页参数非法")
	// ErrQueryResultTooMany 表示物化查询结果超过框架允许的单次行数上限。
	ErrQueryResultTooMany = errors.New("数据库查询结果过大")
	// ErrInvalidAggregateValue 表示数据库返回的聚合值无法精确解析。
	ErrInvalidAggregateValue = errors.New("数据库聚合值非法")
	// ErrInvalidTransaction 表示事务依赖、回调或生命周期状态非法。
	ErrInvalidTransaction = errors.New("数据库事务非法")
	// ErrTransactionDone 表示事务已经提交或回滚。
	ErrTransactionDone = errors.New("数据库事务已结束")
	// ErrInvalidModel 表示模型依赖、配置或输入值非法。
	ErrInvalidModel = errors.New("数据库模型非法")
	// ErrInvalidRelation 表示关联定义或关联数据不完整。
	ErrInvalidRelation = errors.New("数据库关联非法")
	// ErrInvalidDatabaseRow 表示结果列重复或扫描值不满足行映射约束。
	ErrInvalidDatabaseRow = errors.New("数据库结果行非法")
	// ErrUnsupportedCursorKey 表示游标类型的顺序依赖数据库类型或 collation，必须显式提供 codec。
	ErrUnsupportedCursorKey = errors.New("数据库游标键需要显式排序编解码器")
	// ErrInsertIDUnavailable 表示当前写入没有可证明的真实主键。
	ErrInsertIDUnavailable = errors.New("数据库插入主键不可用")
	// ErrPartialWrite 表示数据库写入已经完成，但本地结果绑定失败。
	ErrPartialWrite = errors.New("数据库写入已完成但本地结果绑定失败")
	// ErrUnsafeExpression 表示低层驱动收到无法证明来源安全的表达式。
	ErrUnsafeExpression = errors.New("数据库表达式来源不安全")
	// ErrInvalidOperationResult 表示驱动返回的写入数量或主键状态互相矛盾。
	ErrInvalidOperationResult = errors.New("数据库操作结果非法")
	// ErrUnsupportedFeature 表示当前驱动不能安全实现请求的数据库能力。
	ErrUnsupportedFeature = contract.ErrUnsupportedFeature
	// ErrUnsupportedLockMode 表示锁模式不在框架的类型化白名单内。
	ErrUnsupportedLockMode = contract.ErrUnsupportedLockMode
)
