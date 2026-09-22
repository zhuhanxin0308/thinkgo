package mongo

import "github.com/zhuhanxin0308/thinkgo/v3/db"

// 驱动复用核心的请求、能力和错误身份，不定义另一套数据库协议。
type (
	Connection                        = db.Connection
	ConnectionID                      = db.ConnectionID
	RowStreamingConnection            = db.RowStreamingConnection
	LocationAwareConnection           = db.LocationAwareConnection
	CapabilityProvider                = db.CapabilityProvider
	ModelPrimaryKeyMapper             = db.ModelPrimaryKeyMapper
	ModelPrimaryKeyGenerationProvider = db.ModelPrimaryKeyGenerationProvider
	DriverCapabilities                = db.DriverCapabilities
	InsertIDKind                      = db.InsertIDKind
	SelectRequest                     = db.SelectRequest
	InsertRequest                     = db.InsertRequest
	UpdateRequest                     = db.UpdateRequest
	DeleteRequest                     = db.DeleteRequest
	CountRequest                      = db.CountRequest
	InsertResult                      = db.InsertResult
	UpdateResult                      = db.UpdateResult
	DeleteResult                      = db.DeleteResult
	Predicate                         = db.Predicate
	PredicateNode                     = db.PredicateNode
)

const (
	InsertIDObjectID    = db.InsertIDObjectID
	InsertIDString      = db.InsertIDString
	InsertIDDynamic     = db.InsertIDDynamic
	PredicateFalse      = db.PredicateFalse
	PredicateBoolean    = db.PredicateBoolean
	PredicateComparison = db.PredicateComparison
)

var (
	ErrDatabaseUnavailable     = db.ErrDatabaseUnavailable
	ErrInvalidQuery            = db.ErrInvalidQuery
	ErrInvalidPagination       = db.ErrInvalidPagination
	ErrUnsafeFullTableMutation = db.ErrUnsafeFullTableMutation
	validateIdentifier         = db.ValidateDriverIdentifier
	validateDataKeys           = db.ValidateDriverData
	validateOrderClause        = db.ValidateDriverOrder
)

func NewConnectionID(namespace string) ConnectionID { return db.NewConnectionID(namespace) }
