package migration

import "fmt"

const (
	migrationHistoryTable = "thinkgo_migrations"
	migrationJournalTable = "thinkgo_migration_journal"
	migrationFenceTable   = "thinkgo_migration_fence"
)

type migrationTableDefinition struct {
	name      string
	existsSQL string
	createSQL string
}

func migrationTableStatements(dialect string) (string, string, error) {
	definitions, err := migrationTableDefinitions(dialect)
	if err != nil {
		return "", "", err
	}
	return definitions[0].existsSQL, definitions[0].createSQL, nil
}

func migrationTableDefinitions(dialect string) ([]migrationTableDefinition, error) {
	var existsTemplate string
	var historySQL string
	var journalSQL string
	var fenceSQL string
	switch dialect {
	case "mysql":
		existsTemplate = "SELECT 1 AS present FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?"
		historySQL = "CREATE TABLE " + migrationHistoryTable + " (name VARCHAR(160) NOT NULL PRIMARY KEY, checksum CHAR(64) NOT NULL, batch BIGINT NOT NULL, applied_at TIMESTAMP(6) NOT NULL)"
		journalSQL = "CREATE TABLE " + migrationJournalTable + " (name VARCHAR(160) NOT NULL PRIMARY KEY, checksum CHAR(64) NOT NULL, batch BIGINT NOT NULL, operation VARCHAR(16) NOT NULL, state VARCHAR(16) NOT NULL, fencing_token BIGINT NOT NULL, started_at TIMESTAMP(6) NOT NULL, finished_at TIMESTAMP(6) NULL, failure_code VARCHAR(64) NULL)"
		fenceSQL = "CREATE TABLE " + migrationFenceTable + " (lock_name VARCHAR(160) NOT NULL PRIMARY KEY, fencing_token BIGINT NOT NULL)"
	case "postgres":
		existsTemplate = "SELECT 1 AS present FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?"
		historySQL = "CREATE TABLE " + migrationHistoryTable + " (name VARCHAR(160) NOT NULL PRIMARY KEY, checksum CHAR(64) NOT NULL, batch BIGINT NOT NULL, applied_at TIMESTAMPTZ NOT NULL)"
		journalSQL = "CREATE TABLE " + migrationJournalTable + " (name VARCHAR(160) NOT NULL PRIMARY KEY, checksum CHAR(64) NOT NULL, batch BIGINT NOT NULL, operation VARCHAR(16) NOT NULL, state VARCHAR(16) NOT NULL, fencing_token BIGINT NOT NULL, started_at TIMESTAMPTZ NOT NULL, finished_at TIMESTAMPTZ NULL, failure_code VARCHAR(64) NULL)"
		fenceSQL = "CREATE TABLE " + migrationFenceTable + " (lock_name VARCHAR(160) NOT NULL PRIMARY KEY, fencing_token BIGINT NOT NULL)"
	case "sqlite":
		existsTemplate = "SELECT 1 AS present FROM sqlite_master WHERE type = 'table' AND name = ?"
		historySQL = "CREATE TABLE " + migrationHistoryTable + " (name TEXT NOT NULL PRIMARY KEY, checksum TEXT NOT NULL, batch INTEGER NOT NULL, applied_at DATETIME NOT NULL)"
		journalSQL = "CREATE TABLE " + migrationJournalTable + " (name TEXT NOT NULL PRIMARY KEY, checksum TEXT NOT NULL, batch INTEGER NOT NULL, operation TEXT NOT NULL, state TEXT NOT NULL, fencing_token INTEGER NOT NULL, started_at DATETIME NOT NULL, finished_at DATETIME NULL, failure_code TEXT NULL)"
		fenceSQL = "CREATE TABLE " + migrationFenceTable + " (lock_name TEXT NOT NULL PRIMARY KEY, fencing_token INTEGER NOT NULL)"
	case "sqlserver":
		existsTemplate = "SELECT 1 AS present FROM sys.tables WHERE schema_id = SCHEMA_ID() AND name = ?"
		historySQL = "CREATE TABLE " + migrationHistoryTable + " (name NVARCHAR(160) NOT NULL PRIMARY KEY, checksum CHAR(64) NOT NULL, batch BIGINT NOT NULL, applied_at DATETIME2(6) NOT NULL)"
		journalSQL = "CREATE TABLE " + migrationJournalTable + " (name NVARCHAR(160) NOT NULL PRIMARY KEY, checksum CHAR(64) NOT NULL, batch BIGINT NOT NULL, operation NVARCHAR(16) NOT NULL, state NVARCHAR(16) NOT NULL, fencing_token BIGINT NOT NULL, started_at DATETIME2(6) NOT NULL, finished_at DATETIME2(6) NULL, failure_code NVARCHAR(64) NULL)"
		fenceSQL = "CREATE TABLE " + migrationFenceTable + " (lock_name NVARCHAR(160) NOT NULL PRIMARY KEY, fencing_token BIGINT NOT NULL)"
	case "oracle":
		existsTemplate = "SELECT 1 AS present FROM user_tables WHERE table_name = ?"
		historySQL = "CREATE TABLE " + migrationHistoryTable + " (name VARCHAR2(160) NOT NULL PRIMARY KEY, checksum CHAR(64) NOT NULL, batch NUMBER(19) NOT NULL, applied_at TIMESTAMP(6) NOT NULL)"
		journalSQL = "CREATE TABLE " + migrationJournalTable + " (name VARCHAR2(160) NOT NULL PRIMARY KEY, checksum CHAR(64) NOT NULL, batch NUMBER(19) NOT NULL, operation VARCHAR2(16) NOT NULL, state VARCHAR2(16) NOT NULL, fencing_token NUMBER(19) NOT NULL, started_at TIMESTAMP(6) NOT NULL, finished_at TIMESTAMP(6) NULL, failure_code VARCHAR2(64) NULL)"
		fenceSQL = "CREATE TABLE " + migrationFenceTable + " (lock_name VARCHAR2(160) NOT NULL PRIMARY KEY, fencing_token NUMBER(19) NOT NULL)"
	default:
		return nil, fmt.Errorf("%w: 不支持迁移方言 %q", ErrInvalidMigration, dialect)
	}
	return []migrationTableDefinition{
		{name: migrationHistoryTable, existsSQL: existsTemplate, createSQL: historySQL},
		{name: migrationJournalTable, existsSQL: existsTemplate, createSQL: journalSQL},
		{name: migrationFenceTable, existsSQL: existsTemplate, createSQL: fenceSQL},
	}, nil
}
