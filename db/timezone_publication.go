package db

import "time"

// publishLocation 不持有管理器状态锁调用连接，允许连接回调读取或更新配置。
// 若通知期间配置变化，补发最新值，确保最后返回的通知不会遗留旧时区。
func (database *DB) publishLocation(apply func(*time.Location)) {
	for {
		location := database.Location()
		apply(location)
		if database.Location() == location {
			return
		}
	}
}
