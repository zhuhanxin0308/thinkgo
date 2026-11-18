package framework

import (
	stdctx "context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/config"
	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
	"github.com/zhuhanxin0308/thinkgo/v3/log"
)

type securityWarningDriver struct {
	mu       sync.Mutex
	messages []string
}

func (d *securityWarningDriver) SaveEntries(entries []*log.LogEntry) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, entry := range entries {
		if entry != nil && entry.Level == log.LevelWarning {
			d.messages = append(d.messages, entry.Message)
		}
	}
	return nil
}

func (d *securityWarningDriver) WriteEntry(entry *log.LogEntry) error {
	return d.SaveEntries([]*log.LogEntry{entry})
}

func (d *securityWarningDriver) Close() error { return nil }

func (d *securityWarningDriver) snapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.messages...)
}

// TestProductionSecurityChecksAreCategorizedDeduplicatedAndBlocking 验证生产安全检查分类、去重且会阻断不安全启动。
func TestProductionSecurityChecksAreCategorizedDeduplicatedAndBlocking(t *testing.T) {
	driver := &securityWarningDriver{}
	logger := log.NewLog(driver)
	defer logger.Close()
	app := &App{
		AppName: "warning-app",
		config:  config.NewConfig(),
		log:     logger,
	}
	app.config.Set("app.app_env", "production")
	app.config.Set("app.security_profile", string(SecurityProfileBrowserCookie))
	app.config.Set("app.session_enable", true)
	app.config.Set("app.csrf_enable", false)
	app.config.Set("app.server", map[string]interface{}{"allowed_hosts": []interface{}{}})
	app.config.Set("csrf", map[string]interface{}{})

	app.warnProductionSecurity()
	app.warnProductionSecurity()
	if err := app.StartupError(); err == nil {
		t.Fatal("生产环境不安全配置应进入启动错误")
	}
	if err := logger.Flush(stdctx.Background()); err != nil {
		t.Fatalf("刷新安全告警日志失败: %v", err)
	}
	messages := driver.snapshot()
	if len(messages) != 5 {
		t.Fatalf("同一应用应输出五类安全告警且各一次，实际 %d 条: %#v", len(messages), messages)
	}
	joined := strings.Join(messages, "\n")
	for _, marker := range []string{"Cookie/Session 密钥", "Cookie/Session Cookie 未启用 Secure", "CSRF 密钥", "allowed_hosts", "CSRF 未启用"} {
		if !strings.Contains(joined, marker) {
			t.Fatalf("安全告警缺少分类 %q: %s", marker, joined)
		}
	}
	if strings.Contains(joined, "secret-value") {
		t.Fatal("安全告警不得包含密钥值")
	}
}

// TestProductionSecurityChecksAllowCompleteConfiguration 验证生产环境完整安全配置可以正常通过检查。
func TestProductionSecurityChecksAllowCompleteConfiguration(t *testing.T) {
	driver := &securityWarningDriver{}
	logger := log.NewLog(driver)
	defer logger.Close()
	cookieFactory, err := cookie.NewCookie(map[string]interface{}{
		"secret": strings.Repeat("c", 32),
		"secure": true,
	})
	if err != nil {
		t.Fatalf("创建测试 Cookie 工厂失败: %v", err)
	}
	app := &App{
		config: config.NewConfig(),
		log:    logger,
		cookie: cookieFactory,
	}
	if err := app.config.Set("app.app_env", "production"); err != nil {
		t.Fatalf("写入生产环境配置失败: %v", err)
	}
	if err := app.config.Set("app.security_profile", string(SecurityProfileBrowserCookie)); err != nil {
		t.Fatalf("写入浏览器安全档案失败: %v", err)
	}
	if err := app.config.Set("app.session_enable", true); err != nil {
		t.Fatalf("写入 Session 开关失败: %v", err)
	}
	if err := app.config.Set("app.csrf_enable", true); err != nil {
		t.Fatalf("写入 CSRF 开关失败: %v", err)
	}
	if err := app.config.Set("app.server.allowed_hosts", []interface{}{"example.com"}); err != nil {
		t.Fatalf("写入 Host 白名单失败: %v", err)
	}
	if err := app.config.Set("csrf.secret", strings.Repeat("s", 32)); err != nil {
		t.Fatalf("写入 CSRF 密钥失败: %v", err)
	}
	if err := app.config.Set("csrf.secure", true); err != nil {
		t.Fatalf("写入 CSRF Secure 配置失败: %v", err)
	}

	app.warnProductionSecurity()
	if err := app.StartupError(); err != nil {
		t.Fatalf("完整生产安全配置不应产生启动错误: %v", err)
	}
	if err := logger.Flush(stdctx.Background()); err != nil {
		t.Fatalf("刷新生产安全检查日志失败: %v", err)
	}
	if messages := driver.snapshot(); len(messages) != 0 {
		t.Fatalf("完整生产安全配置不应输出安全告警: %#v", messages)
	}
}

// TestProductionStatelessAPIProfileDoesNotRequireCookieOrCSRF 验证无状态 API 不会被无关的浏览器 Cookie 安全要求阻断。
func TestProductionStatelessAPIProfileDoesNotRequireCookieOrCSRF(t *testing.T) {
	driver := &securityWarningDriver{}
	logger := log.NewLog(driver)
	defer logger.Close()
	app := &App{config: config.NewConfig(), log: logger}
	app.config.Set("app.app_env", "production")
	app.config.Set("app.security_profile", string(SecurityProfileStatelessAPI))
	app.config.Set("app.session_enable", false)
	app.config.Set("app.csrf_enable", false)
	app.config.Set("app.server.allowed_hosts", []interface{}{"api.example.com"})
	app.applyApplicationRuntimeConfig()

	app.warnProductionSecurity()
	if err := app.StartupError(); err != nil {
		t.Fatalf("完整无状态 API 安全档案不应要求 Cookie 或 CSRF: %v", err)
	}
	if err := logger.Flush(stdctx.Background()); err != nil {
		t.Fatalf("刷新无状态安全检查日志失败: %v", err)
	}
	if messages := driver.snapshot(); len(messages) != 0 {
		t.Fatalf("完整无状态 API 安全档案不应输出告警: %#v", messages)
	}
}

// TestProductionSecurityRejectsWildcardAllowedHosts 验证生产环境不能用 * 代替 Host 白名单。
func TestProductionSecurityRejectsWildcardAllowedHosts(t *testing.T) {
	driver := &securityWarningDriver{}
	logger := log.NewLog(driver)
	defer logger.Close()
	cookieFactory, err := cookie.NewCookie(map[string]interface{}{
		"secret": strings.Repeat("c", 32),
		"secure": true,
	})
	if err != nil {
		t.Fatalf("创建测试 Cookie 工厂失败: %v", err)
	}
	app := &App{
		config: config.NewConfig(),
		log:    logger,
		cookie: cookieFactory,
	}
	if err := app.config.Set("app.app_env", "production"); err != nil {
		t.Fatalf("写入生产环境配置失败: %v", err)
	}
	if err := app.config.Set("app.security_profile", string(SecurityProfileBrowserCookie)); err != nil {
		t.Fatalf("写入浏览器安全档案失败: %v", err)
	}
	if err := app.config.Set("app.session_enable", true); err != nil {
		t.Fatalf("写入 Session 开关失败: %v", err)
	}
	if err := app.config.Set("app.csrf_enable", true); err != nil {
		t.Fatalf("写入 CSRF 开关失败: %v", err)
	}
	if err := app.config.Set("app.server.allowed_hosts", []interface{}{"*"}); err != nil {
		t.Fatalf("写入 Host 配置失败: %v", err)
	}
	if err := app.config.Set("csrf.secret", strings.Repeat("s", 32)); err != nil {
		t.Fatalf("写入 CSRF 密钥失败: %v", err)
	}
	if err := app.config.Set("csrf.secure", true); err != nil {
		t.Fatalf("写入 CSRF Secure 配置失败: %v", err)
	}

	app.warnProductionSecurity()
	if err := app.StartupError(); err == nil {
		t.Fatal("生产环境使用 allowed_hosts=* 必须进入启动错误")
	}
}

// TestSecurityWarningsUseResolvedConfig 验证显式开发/测试环境跳过检查，而缺省空 app_env 执行安全检查并输出告警，
// 且当配置省略 app_env 时通过标准 APP_ENV=production/prod/release 严格阻断不安全启动。
func TestSecurityWarningsUseResolvedConfig(t *testing.T) {
	for _, exempt := range []string{"development", "dev", "local", "testing", "test"} {
		driver := &securityWarningDriver{}
		logger := log.NewLog(driver)
		app := &App{config: config.NewConfig(), log: logger}
		app.config.Set("app.app_env", exempt)
		app.config.Set("app.server", map[string]interface{}{"allowed_hosts": []interface{}{}})
		app.warnProductionSecurity()
		if err := logger.Flush(stdctx.Background()); err != nil {
			t.Fatalf("刷新开发环境告警日志失败: %v", err)
		}
		if messages := driver.snapshot(); len(messages) != 0 {
			t.Fatalf("显式豁免环境 %q 不应输出安全告警: %#v", exempt, messages)
		}
		_ = logger.Close()
	}

	// app_env 缺省为空时，默认执行安全检查并记录去重告警，但不破坏未配置 app_env 的开发/测试启动。
	emptyEnvDriver := &securityWarningDriver{}
	emptyEnvLogger := log.NewLog(emptyEnvDriver)
	defer emptyEnvLogger.Close()
	emptyEnvApp := &App{config: config.NewConfig(), log: emptyEnvLogger}
	emptyEnvApp.config.Set("app.server", map[string]interface{}{"allowed_hosts": []interface{}{}})
	emptyEnvApp.warnProductionSecurity()
	emptyEnvApp.warnProductionSecurity()
	if err := emptyEnvLogger.Flush(stdctx.Background()); err != nil {
		t.Fatalf("刷新缺省环境告警日志失败: %v", err)
	}
	if messages := emptyEnvDriver.snapshot(); len(messages) == 0 {
		t.Fatal("缺省空 app_env 应执行安全检查并输出安全告警")
	}
	if err := emptyEnvApp.StartupError(); err != nil {
		t.Fatalf("缺省空 app_env 在未启用 strict 时不应阻断启动: %v", err)
	}

	// 脚手架配置未声明 app_env 字段时，标准环境变量 APP_ENV=production/prod/release 必须严格阻断不安全启动。
	for _, prodEnv := range []string{"production", "prod", "release"} {
		t.Run("APP_ENV="+prodEnv, func(t *testing.T) {
			basePath := t.TempDir()
			writeTestAppConfigFiles(t, basePath)
			scaffoldAppJSON := `{"default_app":"index","default_timezone":"Asia/Shanghai","with_route":true,"public_path":"public","server":{"host":"127.0.0.1","port":8000},"show_error_msg":false}`
			if err := writePrivateSecurityFile(filepath.Join(basePath, "config", "app.json"), []byte(scaffoldAppJSON)); err != nil {
				t.Fatal(err)
			}
			t.Setenv("APP_ENV", prodEnv)
			t.Setenv("APP_DEBUG", "true")

			app, err := initializeTestConsoleApp(t, basePath)
			if err == nil || app.State() != ApplicationStateFailed {
				t.Fatalf("APP_ENV=%s 下脚手架不安全配置必须阻断启动: state=%s err=%v", prodEnv, app.State(), err)
			}
			for _, marker := range []string{"app_debug", "security_profile", "allowed_hosts"} {
				if !strings.Contains(err.Error(), marker) {
					t.Fatalf("APP_ENV=%s 启动阻断缺少检查项 %q: %v", prodEnv, marker, err)
				}
			}
		})
	}
}

func writePrivateSecurityFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
