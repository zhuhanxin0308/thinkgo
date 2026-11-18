package framework

import (
	"net/netip"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
	"github.com/zhuhanxin0308/thinkgo/v3/config"
	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/debug"
	"github.com/zhuhanxin0308/thinkgo/v3/env"
	"github.com/zhuhanxin0308/thinkgo/v3/event"
	"github.com/zhuhanxin0308/thinkgo/v3/filesystem"
	"github.com/zhuhanxin0308/thinkgo/v3/health"
	"github.com/zhuhanxin0308/thinkgo/v3/lang"
	"github.com/zhuhanxin0308/thinkgo/v3/log"
	"github.com/zhuhanxin0308/thinkgo/v3/metrics"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
	"github.com/zhuhanxin0308/thinkgo/v3/migration"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
	"github.com/zhuhanxin0308/thinkgo/v3/session"
	"github.com/zhuhanxin0308/thinkgo/v3/telemetry"
	"github.com/zhuhanxin0308/thinkgo/v3/view"
)

// appMetadataState 管理应用元数据、多应用编目、项目配置基线与时区/运行起点指标。
type appMetadataState struct {
	metadataMu            sync.RWMutex
	debugOverrideSet      bool
	debugOverride         bool
	applicationName       string
	applicationDefinition ApplicationDefinition
	applicationCatalog    *applicationCatalog
	nativeApplicationPath string
	nativeRuntimeBasePath string
	projectConfiguration  *projectConfigurationState
	projectConfigSnapshot *config.Config
	namespace             string
	baseEnvName           string
	envName               string
	configExt             string
	consoleMode           bool
	beginTime             atomic.Uint64
	beginMem              atomic.Uint64
	locationMu            sync.RWMutex
	location              *time.Location
}

// appServiceHolders 聚合由容器统一托管的核心服务实例快照。
type appServiceHolders struct {
	container             *Container
	route                 *route.Router
	routeFacade           *Route
	middleware            *middleware.Pipeline
	applicationMiddleware *middleware.Pipeline
	db                    *db.DB
	dbManager             *db.Manager
	config                *config.Config
	env                   *env.Env
	log                   *log.Log
	metrics               *metrics.Registry
	health                *health.Registry
	view                  *view.View
	cache                 *cache.Cache
	filesystem            *filesystem.Filesystem
	event                 *event.Dispatcher
	lang                  *lang.Lang
	cookie                *cookie.Cookie
	session               *session.Session
	debug                 *debug.Debug
	migrations            *migration.Registry
	telemetry             *telemetry.Tracing
}

// appStartupState 管理应用启动阶段状态机、一次性事件屏障与启动错误汇聚。
type appStartupState struct {
	appInitOnce         sync.Once
	appInitErr          error
	routeLoadOnce       sync.Once
	routeLoadErr        error
	startupMu           sync.RWMutex
	startupErr          error
	startupErrorCount   int
	startupErrorOmitted int
	lifecycle           appLifecycle
}

// appRegistrationState 管理组件注册表、Provider 生命周期与受控资源释放。
type appRegistrationState struct {
	registry                   applicationRegistry
	registrationMu             sync.RWMutex
	registrationClosed         bool
	loadingNativeDefinition    bool
	serviceMutationMu          sync.Mutex
	resources                  serviceResources
	applicationServiceMu       sync.RWMutex
	applicationServices        []*applicationServiceProvider
	applicationServiceInFlight map[reflect.Type]struct{}
	providers                  providerLifecycle
}

// appRuntimePolicyState 管理安全档案、会话/CSRF/可观测性开关及运维访问白名单。
type appRuntimePolicyState struct {
	securityWarningBits        uint32
	securityEnforcedBits       uint32
	sessionEnabled             bool
	csrfEnabled                bool
	traceEnabled               bool
	metricsEnabled             bool
	operationalRoutesEnabled   bool
	securityProfile            SecurityProfile
	operationalAccess          OperationalAccess
	operationalAllowedPrefixes []netip.Prefix
}

// appDatabaseRuntimeState 管理数据库启动策略、就绪探针错误与控制台跳库开关。
type appDatabaseRuntimeState struct {
	databaseStartupPolicy DatabaseStartupPolicy
	databaseReadinessMu   sync.RWMutex
	databaseReadinessErr  error
	// skipDatabaseInit 为 true 时，Initialize 跳过数据库连接。
	// 供不需要数据库的控制台命令（version/list/make:* 等）使用，
	// 避免每次执行命令都尝试连库并打印连接失败日志。
	skipDatabaseInit bool
}
