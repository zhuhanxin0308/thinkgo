package db

// Connector 数据库连接器接口。
type Connector interface {
	Connect(config Config) (Connection, error)
}

// Config 数据库连接配置。
type Config struct {
	Type                   string            // 数据库类型（mysql/postgres/sqlsrv 等）
	Hostname               string            // 主机地址
	Hostport               string            // 端口号
	Username               string            // 用户名
	Password               string            // 密码
	Database               string            // 数据库名
	Params                 map[string]string // 额外连接参数
	Charset                string            // 字符集
	Prefix                 string            // 表前缀
	MaxOpenConns           int               // 最大打开连接数
	MaxIdleConns           int               // 最大空闲连接数
	ConnMaxLifetimeSeconds int               // 连接最大生命周期（秒）
	ConnMaxIdleTimeSeconds int               // 连接最大空闲时长（秒）
	Debug                  bool              // 是否开启 SQL 调试
	AutoTimestamp          bool              // 是否自动维护时间戳
	CreateTimeField        string            // 创建时间字段名
	UpdateTimeField        string            // 更新时间字段名
	TimestampValueType     string            // 自动时间戳落库值类型（datetime/unix）
}
