package exception

import "net/http"

// CanExposeDebugPage 判断请求是否允许接收调试信息。它只接受回环连接，
// 并排除代理头显示为远程或未知客户端的请求；不使用可伪造的客户端 IP 代替连接地址。
func CanExposeDebugPage(r *http.Request) bool {
	return canExposeDebugPage(r)
}
