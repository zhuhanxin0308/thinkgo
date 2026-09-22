package neo4j

import (
	"reflect"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

// 驱动复用核心的请求、能力和错误身份，不定义另一套数据库协议。
type (
	Connection              = db.Connection
	ConnectionID            = db.ConnectionID
	LocationAwareConnection = db.LocationAwareConnection
	CapabilityProvider      = db.CapabilityProvider
	DriverCapabilities      = db.DriverCapabilities
	InsertIDKind            = db.InsertIDKind
	SelectRequest           = db.SelectRequest
	InsertRequest           = db.InsertRequest
	UpdateRequest           = db.UpdateRequest
	DeleteRequest           = db.DeleteRequest
	CountRequest            = db.CountRequest
	InsertResult            = db.InsertResult
	UpdateResult            = db.UpdateResult
	DeleteResult            = db.DeleteResult
	Predicate               = db.Predicate
	PredicateNode           = db.PredicateNode
)

const (
	InsertIDInteger     = db.InsertIDInteger
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
	ErrInsertIDUnavailable     = db.ErrInsertIDUnavailable
	ErrInvalidAggregateValue   = db.ErrInvalidAggregateValue
	ErrInvalidDatabaseRow      = db.ErrInvalidDatabaseRow
	validateIdentifier         = db.ValidateDriverIdentifier
	validateOrderClause        = db.ValidateDriverOrder
	cloneDatabaseMap           = db.CloneDriverData
)

func NewConnectionID(namespace string) ConnectionID { return db.NewConnectionID(namespace) }

func isZeroDBValue(value interface{}) bool { return value == nil || reflect.ValueOf(value).IsZero() }
