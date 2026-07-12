package db

import "errors"

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
	// ErrUnsafeFullTableMutation 表示更新或删除缺少业务 WHERE 条件。
	ErrUnsafeFullTableMutation = errors.New("禁止无条件全表修改")
	// ErrInvalidPagination 表示分页值为负、越界或发生整数溢出。
	ErrInvalidPagination = errors.New("数据库分页参数非法")
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
)
