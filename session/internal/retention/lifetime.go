// Package retention 定义 Session 和存储驱动共享的回收边界。
package retention

import "time"

// IdleLifetime 是未指定过期时间的浏览器会话在服务器端的空闲回收时限。
const IdleLifetime = 24 * time.Hour
