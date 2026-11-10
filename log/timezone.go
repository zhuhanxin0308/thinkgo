package log

import "time"

// publishLocation 在外部通知完成后复核最新配置，旧通知晚到时补发当前值。
// 并发更新以 stateMu 下最后写入的时区为准；不持锁调用驱动或子通道，允许回调重入。
func (l *Log) publishLocation(apply func(*time.Location)) {
	for {
		l.stateMu.RLock()
		captured := l.location
		l.stateMu.RUnlock()
		location := captured
		if location == nil {
			location = time.UTC
		}
		apply(location)
		l.stateMu.RLock()
		unchanged := l.location == captured
		l.stateMu.RUnlock()
		if unchanged {
			return
		}
	}
}
