package framework

import (
	stdctx "context"
	"strings"
	"sync"
	"testing"

	"thinkgo/framework/config"
	"thinkgo/framework/cookie"
	"thinkgo/framework/env"
	"thinkgo/framework/log"
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
	t.Setenv("APP_ENV", "production")
	driver := &securityWarningDriver{}
	logger := log.NewLog(driver)
	defer logger.Close()
	app := &App{
		ApplicationName: "warning-app",
		config:          config.NewConfig(),
		env:             env.NewEnv(),
		log:             logger,
	}
	app.config.Set("app.app_env", "development")
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
	t.Setenv("APP_ENV", "production")
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
		env:    env.NewEnv(),
		log:    logger,
		cookie: cookieFactory,
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

// TestProductionSecurityRejectsWildcardAllowedHosts 验证生产环境不能用 * 代替 Host 白名单。
func TestProductionSecurityRejectsWildcardAllowedHosts(t *testing.T) {
	t.Setenv("APP_ENV", "production")
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
		env:    env.NewEnv(),
		log:    logger,
		cookie: cookieFactory,
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

// TestSecurityWarningsRespectEnvironmentPrecedence 验证 APP_ENV 优先于配置，开发环境不输出生产告警。
func TestSecurityWarningsRespectEnvironmentPrecedence(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	driver := &securityWarningDriver{}
	logger := log.NewLog(driver)
	defer logger.Close()
	app := &App{config: config.NewConfig(), env: env.NewEnv(), log: logger}
	app.config.Set("app.app_env", "production")
	app.config.Set("app.server", map[string]interface{}{"allowed_hosts": []interface{}{}})
	app.warnProductionSecurity()
	if err := logger.Flush(stdctx.Background()); err != nil {
		t.Fatalf("刷新开发环境告警日志失败: %v", err)
	}
	if messages := driver.snapshot(); len(messages) != 0 {
		t.Fatalf("显式开发环境不应输出生产安全告警: %#v", messages)
	}
}
