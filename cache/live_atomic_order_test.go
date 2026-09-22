//go:build integration

package cache

import (
	"testing"
	"time"
)

// TestLiveRedisAtomicReservationOrder 使用真实服务覆盖预留票据乱序及 WATCH 提交冲突。
func TestLiveRedisAtomicReservationOrder(t *testing.T) {
	manager, _ := liveScopedRedis(t)
	backend, release, err := manager.driver()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, operation := range []string{"update", "increment", "decrement", "remove", "set"} {
		t.Run(operation, func(t *testing.T) { assertAtomicReservationOrder(t, backend, operation) })
	}
	t.Run("watch conflict", func(t *testing.T) { assertAtomicWatchConflict(t, backend) })
	t.Run("watch missing origin", func(t *testing.T) { assertAtomicWatchMissingOrigin(t, backend) })
	t.Run("invalidated ttl", func(t *testing.T) { assertCounterInvalidatedTTLAfter(t, backend, time.Sleep) })
}
