//go:build integration

package db_test

import "testing"

func TestLiveMySQLRawSQLBoundary(t *testing.T) {
	exerciseRawSQLBoundary(t, connectLiveSQL(t, "mysql"), liveSQLTableName())
}

func TestLivePostgreSQLRawSQLBoundary(t *testing.T) {
	exerciseRawSQLBoundary(t, connectLiveSQL(t, "pgsql"), liveSQLTableName())
}
