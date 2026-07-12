package framework

import frameworkVersion "thinkgo/framework/version"

const (
	// Version 是供应用代码读取的当前框架语义版本号。
	Version = frameworkVersion.Number
)

// ==================== 运行时目录常量 ====================

const (
	// RuntimeLogDir 日志目录（相对于 BasePath）
	RuntimeLogDir = "/runtime/log"
	// RuntimeCacheDir 缓存目录（相对于 BasePath）
	RuntimeCacheDir = "/runtime/cache"
)

// ==================== 服务器默认配置常量 ====================

const (
	// DefaultServerHost 默认监听地址
	DefaultServerHost = "0.0.0.0"
	// DefaultServerPort 默认监听端口
	DefaultServerPort = 8080
	// DefaultTLSCertFile 默认 TLS 证书文件路径
	DefaultTLSCertFile = "./runtime/cert.pem"
	// DefaultTLSKeyFile 默认 TLS 私钥文件路径
	DefaultTLSKeyFile = "./runtime/key.pem"
)

// ==================== 数据库默认配置常量 ====================

const (
	// DefaultDBType 默认数据库类型
	DefaultDBType = "mysql"
	// DefaultDBHost 默认数据库主机
	DefaultDBHost = "127.0.0.1"
	// DefaultDBPort 默认数据库端口
	DefaultDBPort = "3306"
	// DefaultDBCharset 默认字符集
	DefaultDBCharset = "utf8mb4"
)

// ==================== CORS 常量 ====================

const (
	// DefaultCorsMaxAge 默认 CORS 预检请求缓存时间（秒）
	DefaultCorsMaxAge = 86400
)

// ==================== 请求处理常量 ====================

const (
	// DefaultMaxUploadSize 默认最大上传文件大小（32MB）
	DefaultMaxUploadSize = 32 << 20
	// DefaultStackTraceSize 默认错误栈帧缓冲区大小
	DefaultStackTraceSize = 4096
)

// ==================== 请求上下文 Key 常量 ====================

const (
	// LangRequestKey 语言上下文键
	LangRequestKey = "_lang"
	// DebugRequestKey 调试实例上下文键
	DebugRequestKey = "_debug"
	// SessionRequestKey 会话实例上下文键
	SessionRequestKey = "_session"
)
