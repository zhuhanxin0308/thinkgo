//go:build oracle

package db

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"testing"

	"github.com/godror/godror"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

type observedLOBReader struct {
	io.Reader
	reads int
}

type oracleLOBSnapshotDriver struct {
	*pinnedSQLDriver
	readers []*observedLOBReader
	values  []string
}

func (recording *oracleLOBSnapshotDriver) Open(name string) (driver.Conn, error) {
	connection, err := recording.pinnedSQLDriver.Open(name)
	if err != nil {
		return nil, err
	}
	return &oracleLOBSnapshotConnection{connection.(*pinnedSQLDriverConnection), recording}, nil
}

type oracleLOBSnapshotConnection struct {
	*pinnedSQLDriverConnection
	recording *oracleLOBSnapshotDriver
}

func (*oracleLOBSnapshotConnection) CheckNamedValue(*driver.NamedValue) error { return nil }

func (connection *oracleLOBSnapshotConnection) ExecContext(_ context.Context, _ string, arguments []driver.NamedValue) (driver.Result, error) {
	for index, argument := range arguments {
		lob, ok := argument.Value.(godror.Lob)
		if !ok || lob.Reader != connection.recording.readers[index] || connection.recording.readers[index].reads != 0 {
			return nil, fmt.Errorf("驱动执行前 LOB 类型、流身份或读取时机被改变")
		}
		value, err := io.ReadAll(lob.Reader)
		if err != nil {
			return nil, err
		}
		connection.recording.values = append(connection.recording.values, string(value))
	}
	return driver.RowsAffected(len(arguments)), nil
}

// TestOracleLOBInsertAllPreservesNativeValues 通过实际 database/sql 派发验证 Oracle 批量参数，不要求 OCI 服务。
func TestOracleLOBInsertAllPreservesNativeValues(t *testing.T) {
	first := &observedLOBReader{Reader: bytes.NewReader([]byte("first"))}
	second := &observedLOBReader{Reader: bytes.NewReader([]byte("second"))}
	recording := &oracleLOBSnapshotDriver{pinnedSQLDriver: &pinnedSQLDriver{state: &pinnedSQLDriverState{}}, readers: []*observedLOBReader{first, second}}
	name := fmt.Sprintf("thinkgo_lob_snapshot_%d", pinnedSQLDriverSequence.Add(1))
	sql.Register(name, recording)
	handle, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	// 零值连接使用直接执行，记录驱动验证实际绑定值而非模拟 OCI 的 Prepare 协议。
	database := NewDB(&SQLConnection{DB: handle, Builder: &builder.Oracle{}})
	defer database.Close()
	affected, err := database.Table("documents").InsertAll([]map[string]interface{}{
		{"payload": godror.Lob{Reader: first}}, {"payload": godror.Lob{Reader: second, IsClob: true}},
	})
	if err != nil || affected != 2 || len(recording.values) != 2 || recording.values[0] != "first" || recording.values[1] != "second" {
		t.Fatalf("原生 LOB 批量执行错误: affected=%d values=%v err=%v", affected, recording.values, err)
	}
}

func (reader *observedLOBReader) Read(buffer []byte) (int, error) {
	reader.reads++
	return reader.Reader.Read(buffer)
}

// TestOracleLOBSnapshotBorrowsStream 验证保留具体 LOB 类型，且快照不提前读取流或借出参数外壳。
func TestOracleLOBSnapshotBorrowsStream(t *testing.T) {
	reader := &observedLOBReader{Reader: bytes.NewReader([]byte("synthetic-lob"))}
	lob := &godror.Lob{Reader: reader, IsClob: true}
	request := newInsertRequest("documents", map[string]interface{}{"payload": lob, "bytes": []byte("owned")}, "id", false)
	lob.IsClob = false
	data, err := request.DataSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	cloned, ok := data["payload"].(*godror.Lob)
	if !ok || cloned == lob || !cloned.IsClob || cloned.Reader != reader || reader.reads != 0 {
		t.Fatal("快照改变 LOB 类型、外壳、流身份或读取时机")
	}
	cloned.IsClob = false
	data["bytes"].([]byte)[0] = 'X'
	again, err := request.DataSnapshot()
	if err != nil || !again["payload"].(*godror.Lob).IsClob || string(again["bytes"].([]byte)) != "owned" {
		t.Fatalf("借用流扩散为参数容器共享: %v", err)
	}
	content, err := io.ReadAll(again["payload"].(*godror.Lob).Reader)
	if err != nil || string(content) != "synthetic-lob" || reader.reads == 0 {
		t.Fatalf("执行期无法读取原生流: %q %v", content, err)
	}
}
