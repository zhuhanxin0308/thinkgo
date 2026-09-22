package env

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestGetOSEnvOverridesFile 验证真实进程环境变量优先于 .env 文件值（12-factor 约定）。
// 这是 `run -p` 在热重载子进程中生效的关键：os.Setenv 注入的 APP_SERVER_PORT
// 必须能覆盖 .env 里的默认端口。
func TestGetOSEnvOverridesFile(t *testing.T) {
	e := NewEnv()
	e.data["SERVER_PORT"] = "8081" // 模拟 .env 文件中的值

	t.Setenv("SERVER_PORT", "9999")

	if got := e.Get("SERVER_PORT"); got != "9999" {
		t.Fatalf("真实环境变量应优先，期望 9999，得到 %q", got)
	}
}

// TestGetEmptyOSEnvStillOverridesFile 验证显式设置为空的进程环境变量也优先于 .env。
func TestGetEmptyOSEnvStillOverridesFile(t *testing.T) {
	e := NewEnv()
	e.data["SERVER_ALLOWED_HOSTS"] = "example.com"

	t.Setenv("SERVER_ALLOWED_HOSTS", "")

	if got := e.Get("SERVER_ALLOWED_HOSTS", "fallback"); got != "" {
		t.Fatalf("空字符串进程环境变量应优先，期望空字符串，得到 %q", got)
	}
}

// TestLookupReportsExplicitEmptyValue 验证 Lookup 能区分显式空值和未设置。
func TestLookupReportsExplicitEmptyValue(t *testing.T) {
	e := NewEnv()
	e.data["DB_PASS"] = "from-file"

	t.Setenv("DB_PASS", "")

	got, ok := e.Lookup("DB_PASS")
	if !ok {
		t.Fatal("显式设置为空的环境变量应报告存在")
	}
	if got != "" {
		t.Fatalf("显式空环境变量应返回空字符串，实际为 %q", got)
	}
}

// TestSnapshotFreezesProcessAndFileEnvironment 验证环境快照同时冻结进程层与
// 文件层，并且替换到其它 Env 后仍保持深拷贝隔离。
func TestSnapshotFreezesProcessAndFileEnvironment(t *testing.T) {
	const processKey = "THINKGO_ENV_SNAPSHOT_PROCESS"
	const fileKey = "THINKGO_ENV_SNAPSHOT_FILE"
	t.Setenv(processKey, "process-before")
	source := NewEnv()
	source.data[fileKey] = "file-before"

	snapshot := source.Snapshot()
	t.Setenv(processKey, "process-after")
	source.data[fileKey] = "file-after"
	if got := snapshot.Get(processKey); got != "process-before" {
		t.Fatalf("环境快照重新读取了进程环境: %q", got)
	}
	if got := snapshot.Get(fileKey); got != "file-before" {
		t.Fatalf("环境快照仍与源文件环境共享数据: %q", got)
	}
	all := snapshot.All()
	all[fileKey] = "caller-mutated"
	if got := snapshot.Get(fileKey); got != "file-before" {
		t.Fatalf("All 返回值修改了环境快照: %q", got)
	}

	target := NewEnv()
	target.data["STALE"] = "value"
	target.ReplaceWithSnapshot(snapshot)
	snapshot.data[fileKey] = "snapshot-mutated"
	if got := target.Get(fileKey); got != "file-before" {
		t.Fatalf("目标环境仍与输入快照共享数据: %q", got)
	}
	if _, exists := target.Lookup("STALE"); exists {
		t.Fatal("替换环境快照后不应保留旧文件环境")
	}
}

// TestGetFallsBackToFile 验证未设置 OS 环境变量时回退到 .env 文件值。
func TestGetFallsBackToFile(t *testing.T) {
	os.Unsetenv("THINKGO_ENV_TEST_KEY")

	e := NewEnv()
	e.data["THINKGO_ENV_TEST_KEY"] = "from-file"

	if got := e.Get("THINKGO_ENV_TEST_KEY"); got != "from-file" {
		t.Fatalf("应回退到 .env 文件值，期望 from-file，得到 %q", got)
	}
}

// TestGetReturnsDefault 验证 OS 环境变量与 .env 都缺失时返回默认值。
func TestGetReturnsDefault(t *testing.T) {
	os.Unsetenv("THINKGO_ENV_MISSING_KEY")

	e := NewEnv()
	if got := e.Get("THINKGO_ENV_MISSING_KEY", "fallback"); got != "fallback" {
		t.Fatalf("应返回默认值，期望 fallback，得到 %q", got)
	}
}

// TestLoadMissingFileIsOptional 验证可选 .env 缺失时不会阻止应用启动。
func TestLoadMissingFileIsOptional(t *testing.T) {
	missing := filepath.Join(t.TempDir(), ".env")
	if err := NewEnv().Load(missing); err != nil {
		t.Fatalf("缺失的可选 .env 应被忽略，实际错误为 %v", err)
	}
}

// TestLoadRejectsMalformedLineWithoutPartialMutation 验证语法错误包含行号且不会留下部分配置。
func TestLoadRejectsMalformedLineWithoutPartialMutation(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	content := "VALID_KEY=before\nMALFORMED_LINE\nAFTER_KEY=after\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("写入测试 .env 失败: %v", err)
	}

	e := NewEnv()
	err := e.Load(file)
	if err == nil {
		t.Fatal("畸形 .env 行必须返回错误")
	}
	if !strings.Contains(err.Error(), file) || !strings.Contains(err.Error(), "2") {
		t.Fatalf("语法错误必须包含文件和行号，实际为 %v", err)
	}
	if _, ok := e.Lookup("VALID_KEY"); ok {
		t.Fatal("加载失败时不得提交语法错误前的部分数据")
	}
}

// TestLoadRejectsInvalidKeyAndUnclosedQuote 验证非法键名和未闭合引号均会失败关闭。
func TestLoadRejectsInvalidKeyAndUnclosedQuote(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "invalid_key", content: "INVALID-KEY=value\n"},
		{name: "unclosed_quote", content: "TOKEN=\"secret\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(file, []byte(test.content), 0o600); err != nil {
				t.Fatalf("写入测试 .env 失败: %v", err)
			}
			if err := NewEnv().Load(file); err == nil || !strings.Contains(err.Error(), "1") {
				t.Fatalf("非法 .env 内容必须返回带行号错误，实际为 %v", err)
			}
		})
	}
}

// TestGetBoolUsesStandardParsing 验证布尔环境变量支持标准大小写和值集合。
func TestGetBoolUsesStandardParsing(t *testing.T) {
	e := NewEnv()
	e.data["FEATURE_TRUE"] = " TRUE "
	e.data["FEATURE_FALSE"] = "0"

	gotTrue, err := e.GetBool("FEATURE_TRUE")
	if err != nil || !gotTrue {
		t.Fatalf("TRUE 应解析为 true，got=%v err=%v", gotTrue, err)
	}
	gotFalse, err := e.GetBool("FEATURE_FALSE", true)
	if err != nil || gotFalse {
		t.Fatalf("0 应解析为 false，got=%v err=%v", gotFalse, err)
	}
	missing, err := e.GetBool("FEATURE_MISSING", true)
	if err != nil || !missing {
		t.Fatalf("缺失键应返回默认值，got=%v err=%v", missing, err)
	}
}

// TestGetBoolRejectsInvalidValue 验证已设置的非法布尔值不会静默回退默认值。
func TestGetBoolRejectsInvalidValue(t *testing.T) {
	e := NewEnv()
	e.data["FEATURE_FLAG"] = "sometimes"

	got, err := e.GetBool("FEATURE_FLAG", true)
	if err == nil {
		t.Fatalf("非法布尔值必须返回错误，实际 got=%v", got)
	}
	if !strings.Contains(err.Error(), "FEATURE_FLAG") || !strings.Contains(err.Error(), "sometimes") {
		t.Fatalf("布尔解析错误必须包含键和值，实际为 %v", err)
	}
}

// TestLoadValidFileCommitsAllValues 验证合法文件会一次性提交空值、引号值和值中等号。
func TestLoadValidFileCommitsAllValues(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	content := strings.Join([]string{
		"# comment",
		"",
		"PLAIN=value",
		"QUOTED=\"secret value\"",
		"SINGLE='single value'",
		"TOKEN=header.payload=signature",
		"EMPTY=",
	}, "\n")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("写入测试 .env 失败: %v", err)
	}

	e := NewEnv()
	if err := e.Load(file); err != nil {
		t.Fatalf("合法 .env 不应加载失败: %v", err)
	}
	wants := map[string]string{
		"PLAIN":  "value",
		"QUOTED": "secret value",
		"SINGLE": "single value",
		"TOKEN":  "header.payload=signature",
		"EMPTY":  "",
	}
	for key, want := range wants {
		got, ok := e.Lookup(key)
		if !ok || got != want {
			t.Fatalf("环境变量 %s 加载错误，want=%q got=%q ok=%v", key, want, got, ok)
		}
	}
}

// TestLoadSupportsThinkPHPSections 验证 ThinkPHP 二级环境变量可以通过“段落.键”读取。
func TestLoadSupportsThinkPHPSections(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	content := strings.Join([]string{
		"[DATABASE1] ",
		"USERNAME =  root",
		"PASSWORD =  123456",
		"",
		"[DATABASE2] ",
		"USERNAME =  root",
		"PASSWORD =  123456 ",
	}, "\n")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("写入段落环境配置失败: %v", err)
	}
	for _, key := range []string{"DATABASE1_USERNAME", "DATABASE1_PASSWORD", "DATABASE2_USERNAME", "DATABASE2_PASSWORD"} {
		value, exists := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("清理测试系统环境变量 %s 失败: %v", key, err)
		}
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(key, value)
				return
			}
			_ = os.Unsetenv(key)
		})
	}

	e := NewEnv()
	if err := e.Load(file); err != nil {
		t.Fatalf("ThinkPHP 段落环境配置不应加载失败: %v", err)
	}
	wants := map[string]string{
		"database1.username": "root",
		"database1.password": "123456",
		"database2.username": "root",
		"database2.password": "123456",
	}
	for key, want := range wants {
		if got := e.Get(key); got != want {
			t.Fatalf("二级环境变量 %s 读取错误，want=%q got=%q", key, want, got)
		}
	}
}

// TestLoadMapsLegacyDatabaseFieldsToDefaultMySQL 验证旧式单连接数据库段落仍能覆盖规范 mysql 配置。
func TestLoadMapsLegacyDatabaseFieldsToDefaultMySQL(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	content := strings.Join([]string{
		"[DATABASE]",
		"TYPE=mysql",
		"HOSTNAME=192.168.3.100",
		"HOSTPORT=3306",
		"USERNAME=root",
		"PASSWORD=secret",
		"DATABASE=user_center",
		"MAX_OPEN_CONNS=32",
		"PARAMS={\"tls\":\"false\"}",
	}, "\n")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("写入旧式数据库环境配置失败: %v", err)
	}
	environment := NewEnv()
	if err := environment.Load(file); err != nil {
		t.Fatalf("旧式数据库环境配置不应加载失败: %v", err)
	}
	wants := map[string]string{
		"DATABASE_CONNECTIONS_MYSQL_TYPE":           "mysql",
		"DATABASE_CONNECTIONS_MYSQL_HOSTNAME":       "192.168.3.100",
		"DATABASE_CONNECTIONS_MYSQL_HOSTPORT":       "3306",
		"DATABASE_CONNECTIONS_MYSQL_USERNAME":       "root",
		"DATABASE_CONNECTIONS_MYSQL_PASSWORD":       "secret",
		"DATABASE_CONNECTIONS_MYSQL_DATABASE":       "user_center",
		"DATABASE_CONNECTIONS_MYSQL_MAX_OPEN_CONNS": "32",
		"DATABASE_CONNECTIONS_MYSQL_PARAMS":         `{"tls":"false"}`,
	}
	for key, want := range wants {
		if got := environment.Get(key); got != want {
			t.Fatalf("旧式数据库键 %s 映射错误，want=%q got=%q", key, want, got)
		}
	}
}

// TestLoadRejectsLegacyDatabaseCollision 验证旧式字段与规范字段同时存在时不会静默覆盖。
func TestLoadRejectsLegacyDatabaseCollision(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	content := "[DATABASE]\nTYPE=mysql\nCONNECTIONS_MYSQL_TYPE=mysql\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("写入冲突数据库环境配置失败: %v", err)
	}
	if err := NewEnv().Load(file); !errors.Is(err, ErrEnvDuplicateKey) {
		t.Fatalf("旧式与规范数据库键冲突应返回 ErrEnvDuplicateKey，实际为 %v", err)
	}
}

// TestLookupFallsBackToLegacyDatabaseOSKey 验证系统环境中的旧式数据库键也能被规范路径读取。
func TestLookupFallsBackToLegacyDatabaseOSKey(t *testing.T) {
	t.Setenv("DATABASE_HOSTNAME", "192.168.3.100")
	if got := NewEnv().Get("DATABASE_CONNECTIONS_MYSQL_HOSTNAME"); got != "192.168.3.100" {
		t.Fatalf("旧式系统数据库键未回退到规范路径，实际为 %q", got)
	}
}

// TestGetDottedNameKeepsOSEnvironmentPrecedence 验证名称规范化后仍保持系统环境变量最高优先级。
func TestGetDottedNameKeepsOSEnvironmentPrecedence(t *testing.T) {
	e := NewEnv()
	e.data["APP_DEBUG"] = "false"
	t.Setenv("APP_DEBUG", "true")

	if got := e.Get("app.debug"); got != "true" {
		t.Fatalf("系统 APP_DEBUG 应覆盖文件段落值，实际为 %q", got)
	}
}

// TestLoadRejectsDuplicateExpandedSectionKey 验证扁平键与段落展开键冲突时原子失败。
func TestLoadRejectsDuplicateExpandedSectionKey(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	content := "APP_DEBUG=false\n[app]\ndebug=true\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("写入重复段落环境键失败: %v", err)
	}

	e := NewEnv()
	e.data["EXISTING"] = "preserved"
	err := e.Load(file)
	if !errors.Is(err, ErrEnvDuplicateKey) {
		t.Fatalf("段落展开后的重复键应返回 ErrEnvDuplicateKey，实际为 %v", err)
	}
	if _, ok := e.Lookup("APP_DEBUG"); ok {
		t.Fatal("段落键冲突后不得提交部分配置")
	}
	if got := e.Get("EXISTING"); got != "preserved" {
		t.Fatalf("加载失败不应破坏已有配置，实际为 %q", got)
	}
}

// TestLoadRejectsMalformedSection 验证段落名必须完整且符合环境变量命名规则。
func TestLoadRejectsMalformedSection(t *testing.T) {
	tests := []string{"[]\nKEY=value\n", "[APP\nKEY=value\n", "[APP-ENV]\nKEY=value\n"}
	for _, content := range tests {
		file := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatalf("写入非法段落环境配置失败: %v", err)
		}
		if err := NewEnv().Load(file); err == nil || !strings.Contains(err.Error(), "1") {
			t.Fatalf("非法段落必须返回第 1 行解析错误，content=%q err=%v", content, err)
		}
	}
}

// TestLoadRejectsDuplicateCaseInsensitiveKeys 验证不同大小写的重复环境键不会静默覆盖。
func TestLoadRejectsDuplicateCaseInsensitiveKeys(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(file, []byte("APP_MODE=first\napp_mode=second\n"), 0o600); err != nil {
		t.Fatalf("写入重复环境键失败: %v", err)
	}
	e := NewEnv()
	if err := e.Load(file); !errors.Is(err, ErrEnvDuplicateKey) {
		t.Fatalf("重复环境键应返回 ErrEnvDuplicateKey，实际为 %v", err)
	}
	if _, ok := e.Lookup("APP_MODE"); ok {
		t.Fatal("重复环境键加载失败后不应提交部分数据")
	}
}

// TestLoadRejectsOversizedFile 验证环境文件超过总字节限制时不会进入解析流程。
func TestLoadRejectsOversizedFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	content := strings.Repeat("A", int(maxEnvFileBytes)+1)
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("写入超大环境文件失败: %v", err)
	}
	if err := NewEnv().Load(file); !errors.Is(err, ErrEnvFileTooLarge) {
		t.Fatalf("超大环境文件应返回 ErrEnvFileTooLarge，实际为 %v", err)
	}
}

// TestLoadRejectsWorldReadableEnv 验证 Unix .env 默认只允许所有者访问，
// 防止部署凭据被同机其他用户读取；Windows ACL 由平台安全门禁负责。
func TestLoadRejectsWorldReadableEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 文件 ACL 不由 Unix mode 位表示")
	}
	file := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(file, []byte("SECRET=value\n"), 0o644); err != nil {
		t.Fatalf("写入权限测试环境文件失败: %v", err)
	}
	if err := NewEnv().Load(file); !errors.Is(err, ErrEnvPermissionsTooOpen) {
		t.Fatalf("权限过宽的 .env 必须被拒绝: %v", err)
	}
}

// TestLoadRejectsSymlink 验证环境文件符号链接不会被跟随读取。
func TestLoadRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.env")
	link := filepath.Join(dir, ".env")
	if err := os.WriteFile(target, []byte("SECRET=value\n"), 0o600); err != nil {
		t.Fatalf("写入符号链接目标失败: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("当前 Windows 环境不允许创建符号链接: %v", err)
		}
		t.Fatalf("创建环境文件符号链接失败: %v", err)
	}
	if err := NewEnv().Load(link); !errors.Is(err, ErrEnvSymlinkNotAllowed) {
		t.Fatalf("环境文件符号链接应返回 ErrEnvSymlinkNotAllowed，实际为 %v", err)
	}
}

// TestGetBoolMissingWithoutDefault 验证未设置布尔变量且无默认值时返回 false。
func TestGetBoolMissingWithoutDefault(t *testing.T) {
	got, err := NewEnv().GetBool("THINKGO_UNSET_BOOL")
	if err != nil || got {
		t.Fatalf("缺失布尔变量应返回 false,nil，got=%v err=%v", got, err)
	}
}

// TestLoadDirectoryReturnsReadError 验证非普通文件产生的读取错误不会被当作缺失配置忽略。
func TestLoadDirectoryReturnsReadError(t *testing.T) {
	err := NewEnv().Load(t.TempDir())
	if err == nil {
		t.Fatal("把目录作为 .env 加载必须返回读取错误")
	}
}

// TestZeroValueEnvCanLoad 验证导出类型的零值也能安全加载配置。
func TestZeroValueEnvCanLoad(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(file, []byte("ZERO_VALUE=ready\n"), 0o600); err != nil {
		t.Fatalf("写入测试 .env 失败: %v", err)
	}

	var e Env
	if err := e.Load(file); err != nil {
		t.Fatalf("Env 零值加载失败: %v", err)
	}
	if got := e.Get("ZERO_VALUE"); got != "ready" {
		t.Fatalf("Env 零值未保存加载结果，实际为 %q", got)
	}
}
