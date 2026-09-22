//go:build integration

package http

import (
	"encoding/json"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/binding"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/connector"
)

// TestLiveMySQLRequestBodyBoundary 通过真实 TCP 和 MySQL 验证四种输入路径及非法正文的写入隔离。
func TestLiveMySQLRequestBodyBoundary(t *testing.T) {
	const prefix = "THINKGO_LIVE_MYSQL_"
	const requestTimeout = 5 * time.Second
	if _, configured := os.LookupEnv(prefix + "HOST"); !configured {
		t.Skip("THINKGO_LIVE_MYSQL_* 未配置")
	}
	for _, suffix := range []string{"PORT", "DATABASE", "USER", "PASSWORD"} {
		if _, configured := os.LookupEnv(prefix + suffix); !configured {
			t.Fatalf("真实 MySQL 环境缺少 %s", suffix)
		}
	}
	connection, err := (&connector.Mysql{}).Connect(db.Config{
		Type: "mysql", Hostname: os.Getenv(prefix + "HOST"), Hostport: os.Getenv(prefix + "PORT"),
		Database: os.Getenv(prefix + "DATABASE"), Username: os.Getenv(prefix + "USER"),
		Password: os.Getenv(prefix + "PASSWORD"), Charset: "utf8mb4", Params: map[string]string{"tls": "false"},
	})
	if err != nil {
		t.Fatal(err)
	}
	database := db.NewDB(connection)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	// 名称完全由本次进程生成，只清理本次创建的隔离表。
	table := fmt.Sprintf("tg_request_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := database.Execute("CREATE TABLE " + table + " (id BIGINT PRIMARY KEY, name VARCHAR(128) NOT NULL) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := database.Execute("DROP TABLE " + table); err != nil {
			t.Error(err)
		}
	})
	type input struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	var calls atomic.Int64
	modes := []string{"raw", "json", "binding", "parameters"}
	for _, mode := range modes {
		_, err := mustHTTPRoute(t, application).Post("/boundary/"+mode, func(request *fwcontext.Request) *fwcontext.Response {
			calls.Add(1)
			var value input
			var decodeErr error
			switch mode {
			case "raw":
				decodeErr = json.NewDecoder(request.Raw().Body).Decode(&value)
			case "json":
				decodeErr = request.Json(&value)
			case "binding":
				decodeErr = binding.Bind(request, &value)
			case "parameters":
				value.ID, value.Name = int64(request.ParamInt("id", 0)), request.Post("name")
			}
			if decodeErr != nil {
				return fwcontext.NewResponse().Code(stdhttp.StatusBadRequest).Content(decodeErr.Error())
			}
			if _, err := database.Table(table).Insert(map[string]interface{}{"id": value.ID, "name": value.Name}); err != nil {
				return fwcontext.NewResponse().Code(stdhttp.StatusInternalServerError).Content(err.Error())
			}
			return fwcontext.NewResponse().Json(value)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	host := newTestHTTPHandler(t, application)
	server := httptest.NewServer(host)
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = requestTimeout
	for index, mode := range modes {
		body := fmt.Sprintf(`{"id":%d,"name":"alice"}`, index+1)
		for _, fixture := range []struct {
			body   string
			status int
		}{
			{body, stdhttp.StatusOK},
			{`{"id":10,"id":11,"name":"invalid"}`, stdhttp.StatusBadRequest},
			{`{"id":10,"name":"invalid","nested":{"x":1,"x":2}}`, stdhttp.StatusBadRequest},
			{`[]`, stdhttp.StatusBadRequest},
			{body + `{}`, stdhttp.StatusBadRequest},
			{`{"id":`, stdhttp.StatusBadRequest},
		} {
			response, err := client.Post(server.URL+"/boundary/"+mode, "application/json", strings.NewReader(fixture.body))
			if err != nil {
				t.Fatal(err)
			}
			payload, readErr := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			if readErr != nil || closeErr != nil || response.StatusCode != fixture.status {
				t.Fatalf("输入路径 %s 响应错误: status=%d body=%s read=%v close=%v", mode, response.StatusCode, payload, readErr, closeErr)
			}
			if fixture.status == stdhttp.StatusOK && string(payload) != body {
				t.Fatalf("输入路径 %s 绑定值改变: %s", mode, payload)
			}
		}
		row, err := database.Table(table).WhereField("id", "=", index+1).Find()
		name := fmt.Sprint(row["name"])
		if encoded, ok := row["name"].([]byte); ok {
			name = string(encoded)
		}
		if err != nil || row == nil || name != "alice" {
			t.Fatalf("输入路径 %s 未正确持久化: row=%v err=%v", mode, row, err)
		}
	}
	if count, err := database.Table(table).Count(); err != nil || count != int64(len(modes)) || calls.Load() != int64(len(modes)) {
		t.Fatalf("非法正文进入业务或数据库: rows=%d calls=%d err=%v", count, calls.Load(), err)
	}
}
