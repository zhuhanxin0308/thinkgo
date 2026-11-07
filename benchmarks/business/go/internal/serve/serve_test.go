package serve

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

type closingProbe struct {
	order *[]string
	name  string
	err   error
}

func (p closingProbe) Close() error {
	*p.order = append(*p.order, p.name)
	return p.err
}

type applicationProbe struct {
	closingProbe
	err error
}

func (p applicationProbe) Run(context.Context) error { return p.err }

func TestOwnedLifecycleClosesApplicationBeforeDependencies(t *testing.T) {
	var order []string
	startErr := errors.New("监听失败")
	closeErr := errors.New("资源关闭失败")
	app := applicationProbe{closingProbe{&order, "application", closeErr}, startErr}
	err := RunOwned(context.Background(), app, closingProbe{&order, "dependencies", nil})
	if !errors.Is(err, startErr) || !errors.Is(err, closeErr) || len(order) != 2 || order[0] != "application" || order[1] != "dependencies" {
		t.Fatalf("错误或关闭顺序丢失: %v %v", err, order)
	}
}

func TestHTTPServerRejectsOccupiedAddressAndCanceledContext(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if err := HTTP(context.Background(), listener.Addr().String(), handler, time.Second); err == nil {
		t.Fatal("占用地址未返回监听失败")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := HTTP(ctx, "127.0.0.1:0", handler, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消语义丢失: %v", err)
	}
}

func TestHTTPServerDrainsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	go func() { result <- serveListener(ctx, listener, handler, time.Second) }()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatal(response.StatusCode)
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("关闭未完成")
	}
}
