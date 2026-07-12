package connector

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"thinkgo/framework/db"
	"thinkgo/framework/db/builder"

	_ "github.com/mattn/go-sqlite3"
)

// Sqlite 连接器会验证文件路径并启用外键、忙等待和 WAL。
type Sqlite struct{}

func (s *Sqlite) Connect(config db.Config) (db.Connection, error) {
	validated, err := validateConnectorConfig(config, "sqlite", false, true)
	if err != nil {
		return nil, err
	}
	if validated.Database == ":memory:" {
		if validated.MaxOpenConns > 1 || validated.MaxIdleConns > 1 {
			return nil, fmt.Errorf("%w: :memory: SQLite 只能使用一个池连接", db.ErrInvalidDatabaseConfig)
		}
		validated.MaxOpenConns = 1
		validated.MaxIdleConns = 1
	}
	dsn, err := buildSqliteDSN(validated)
	if err != nil {
		return nil, err
	}
	created, err := prepareSqliteFile(validated.Database)
	if err != nil {
		return nil, err
	}
	connection, err := openSQLConnection("sqlite3", dsn, &builder.Sqlite{}, validated)
	if err != nil && created {
		if removeErr := os.Remove(validated.Database); removeErr != nil && !os.IsNotExist(removeErr) {
			return nil, fmt.Errorf("%w；清理新建 SQLite 文件失败: %v", err, removeErr)
		}
	}
	return connection, err
}

func buildSqliteDSN(config db.Config) (string, error) {
	if config.Database != ":memory:" && (strings.ContainsAny(config.Database, "?#") || strings.HasPrefix(strings.ToLower(config.Database), "file:")) {
		return "", fmt.Errorf("%w: SQLite database 必须是普通文件路径，不能包含 URI 参数", db.ErrInvalidDatabaseConfig)
	}
	params := map[string]string{
		"_busy_timeout": "5000",
		"_foreign_keys": "on",
	}
	if config.Database != ":memory:" {
		params["_journal_mode"] = "WAL"
	}
	for key, value := range config.Params {
		params[key] = value
	}
	query := url.Values{}
	for key, value := range params {
		query.Set(key, value)
	}
	return config.Database + "?" + query.Encode(), nil
}

func prepareSqliteFile(path string) (bool, error) {
	if path == ":memory:" {
		return false, nil
	}
	cleaned := filepath.Clean(path)
	info, err := os.Lstat(cleaned)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, fmt.Errorf("%w: SQLite 路径必须是普通文件", db.ErrInvalidDatabaseConfig)
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(cleaned, 0o600); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	if !os.IsNotExist(err) {
		return false, err
	}
	handle, err := os.OpenFile(cleaned, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	if err := handle.Close(); err != nil {
		_ = os.Remove(cleaned)
		return false, err
	}
	return true, nil
}

func init() {
	mustRegisterConnector("sqlite", &Sqlite{})
}
