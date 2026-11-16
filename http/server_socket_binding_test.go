package http

import (
	"net"
	"strings"
	"testing"
)

func TestHTTP3DynamicPortIsSharedAndFixedPortConflictReleasesTCP(t *testing.T) {
	tcp, udp, err := listenConfiguredServerSockets("127.0.0.1:0", serverConf{EnableHTTP3: true})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	address := tcp.Addr().String()
	if udp.LocalAddr().String() != address {
		_ = tcp.Close()
		t.Fatalf("TCP 和 UDP 必须使用同一实际地址: TCP=%s UDP=%s", address, udp.LocalAddr())
	}
	if err := tcp.Close(); err != nil {
		t.Fatal(err)
	}
	port := udp.LocalAddr().(*net.UDPAddr).Port
	fixedTCP, fixedUDP, err := listenConfiguredServerSockets(address, serverConf{EnableHTTP3: true, Port: port})
	if err == nil || fixedTCP != nil || fixedUDP != nil || !strings.Contains(err.Error(), "UDP 监听失败") {
		t.Fatalf("固定端口冲突必须报 UDP 绑定错误: %v, %v, %v", fixedTCP, fixedUDP, err)
	}
	reusable, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("UDP 绑定失败后 TCP 端口应已释放: %v", err)
	}
	_ = reusable.Close()

	// 让动态模式从已被 UDP 占用的候选开始，模拟只对一个协议可用的系统分配。
	retryTCP, retryUDP, err := listenConfiguredServerSockets(address, serverConf{Host: "127.0.0.1", EnableHTTP3: true})
	if err != nil {
		t.Fatal(err)
	}
	defer retryTCP.Close()
	defer retryUDP.Close()
	if retryTCP.Addr().String() == address || retryTCP.Addr().String() != retryUDP.LocalAddr().String() {
		t.Fatalf("动态模式应选取另一组同号 TCP/UDP 监听: TCP=%s UDP=%s", retryTCP.Addr(), retryUDP.LocalAddr())
	}
}
