package db

import (
	"sync/atomic"
	"testing"
	"time"
)

// delayedLocationConnection 在第一次通知时暂停，暴露外部连接通知的乱序窗口。
type delayedLocationConnection struct {
	SQLConnection
	first   atomic.Bool
	entered chan struct{}
	resume  chan struct{}
	read    func()
}

func (connection *delayedLocationConnection) SetLocation(location *time.Location) {
	if connection.first.CompareAndSwap(false, true) {
		close(connection.entered)
		<-connection.resume
	}
	connection.read()
	connection.SQLConnection.SetLocation(location)
}

// TestDatabaseLocationRepairsStalePublication 验证旧通知返回后重新同步最新配置，且允许回调读取管理器。
func TestDatabaseLocationRepairsStalePublication(t *testing.T) {
	connection := &delayedLocationConnection{entered: make(chan struct{}), resume: make(chan struct{})}
	database := NewDB(connection)
	connection.read = func() { _ = database.Location() }
	latest, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		database.SetLocation(nil)
		close(done)
	}()
	<-connection.entered
	database.SetLocation(latest)
	close(connection.resume)
	<-done
	if database.Location() != latest || connection.Location() != latest {
		t.Fatalf("旧通知覆盖最新配置: database=%v connection=%v", database.Location(), connection.Location())
	}
	// 跨夏令时边界验证连接用于写入的本地时间仍与管理器一致。
	instant := time.Date(2026, time.March, 8, 7, 30, 0, 0, time.UTC)
	if got := instant.In(connection.Location()); got.Format(time.RFC3339) != "2026-03-08T03:30:00-04:00" {
		t.Fatalf("连接的夏令时时间解释错误: %v", got)
	}
}

// reentrantLegacyLocationConnection 保留旧接口形态，并在回调中更新管理器配置。
type reentrantLegacyLocationConnection struct {
	timestampCaptureConnection
	current *time.Location
	apply   func()
}

func (connection *reentrantLegacyLocationConnection) SetLocation(location *time.Location) {
	if connection.apply != nil {
		apply := connection.apply
		connection.apply = nil
		apply()
	}
	connection.current = location
}

// TestDatabaseLocationSupportsReentrantLegacySetter 验证无状态锁调用外部连接，并修复重入后的旧值。
func TestDatabaseLocationSupportsReentrantLegacySetter(t *testing.T) {
	connection := &reentrantLegacyLocationConnection{}
	database := NewDB(connection)
	latest, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	connection.apply = func() { database.SetLocation(latest) }
	database.SetLocation(nil)
	if connection.current != latest || database.Location() != latest {
		t.Fatalf("重入更新被旧通知覆盖: database=%v connection=%v", database.Location(), connection.current)
	}
}
