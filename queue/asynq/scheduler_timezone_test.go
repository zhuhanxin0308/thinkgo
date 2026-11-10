package asynq

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	backend "github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/zhuhanxin0308/thinkgo/v3/queue"
)

// TestSchedulerRejectsCustomUTC 验证两个构造入口都拒绝伪装为 UTC 的自定义时区。
func TestSchedulerRejectsCustomUTC(t *testing.T) {
	const secondsPerHour = int(time.Hour / time.Second)
	copiedUTC := *time.UTC
	seasonal := schedulerSeasonalUTCTestLocation(t)
	for _, location := range []*time.Location{
		time.FixedZone("UTC", 8*secondsPerHour),
		time.FixedZone("UTC", -5*secondsPerHour),
		time.FixedZone("UTC", 0),
		&copiedUTC,
		seasonal,
	} {
		server := miniredis.RunT(t)
		client := redis.NewClient(&redis.Options{Addr: server.Addr()})
		t.Cleanup(func() { _ = client.Close() })
		for _, constructor := range []func() (*Scheduler, error){
			func() (*Scheduler, error) { return NewScheduler(RedisConfig{Address: server.Addr()}, location) },
			func() (*Scheduler, error) { return NewSchedulerFromRedisClient(client, location) },
		} {
			scheduler, err := constructor()
			if scheduler != nil {
				_ = scheduler.Shutdown()
			}
			if scheduler != nil || !errors.Is(err, queue.ErrInvalidOptions) {
				t.Errorf("自定义 UTC 必须在创建调度器前被拒绝: location=%v scheduler=%v err=%v", location, scheduler, err)
			}
		}
		if len(server.Keys()) != 0 {
			t.Fatal("拒绝时区的构造过程写入了 Redis")
		}
	}
}

// TestSchedulerStandardUTCSchedulesAtUTC 验证两个入口的真实后端按 UTC 计算跨日固定时刻。
func TestSchedulerStandardUTCSchedulesAtUTC(t *testing.T) {
	loaded, err := time.LoadLocation("UTC")
	if err != nil {
		t.Fatal(err)
	}
	for _, borrowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "标准 UTC", true: "加载 UTC"}[borrowed], func(t *testing.T) {
			t.Parallel()
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			var scheduler *Scheduler
			var err error
			if borrowed {
				scheduler, err = NewSchedulerFromRedisClient(client, loaded)
			} else {
				scheduler, err = NewScheduler(RedisConfig{Address: server.Addr()}, time.UTC)
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = scheduler.Shutdown() })
			task, err := queue.NewTask("timezone.fixture", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := scheduler.Register("0 0 * * *", task, queue.EnqueueOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := scheduler.Start(); err != nil {
				t.Fatal(err)
			}
			inspector := backend.NewInspectorFromRedisClient(client)
			const heartbeatDeadline = 20 * time.Second
			const pollInterval = 20 * time.Millisecond
			deadline := time.Now().Add(heartbeatDeadline)
			for time.Now().Before(deadline) {
				entries, err := inspector.SchedulerEntries()
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) == 1 {
					next := entries[0].Next.UTC()
					if next.IsZero() || next.Hour() != 0 || next.Minute() != 0 || next.Second() != 0 {
						t.Fatalf("后端未按 UTC 午夜调度: %v", next)
					}
					return
				}
				time.Sleep(pollInterval)
			}
			t.Fatal("未读取到运行中调度器的计划")
		})
	}
}

// schedulerSeasonalUTCTestLocation 构造冬季零偏移、夏季非零偏移且名称为 UTC 的合法 TZif 数据。
func schedulerSeasonalUTCTestLocation(t *testing.T) *time.Location {
	t.Helper()
	const reservedHeaderBytes = 15
	const abbreviationBytes = "UTC\x00DST\x00"
	data := bytes.NewBufferString("TZif\x00")
	data.Write(make([]byte, reservedHeaderBytes))
	write := func(value interface{}) {
		if err := binary.Write(data, binary.BigEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	// TZif v1 的计数依次为 UT/local、标准/夏令、闰秒、转换、类型及名称字节数。
	write([6]uint32{0, 0, 0, 2, 2, uint32(len(abbreviationBytes))})
	for _, month := range []time.Month{time.March, time.November} {
		write(int32(time.Date(2026, month, 1, 0, 0, 0, 0, time.UTC).Unix()))
	}
	data.Write([]byte{1, 0})
	write(int32(0))
	data.Write([]byte{0, 0})
	write(int32(time.Hour / time.Second))
	data.Write([]byte{1, byte(len("UTC\x00"))})
	data.WriteString(abbreviationBytes)
	location, err := time.LoadLocationFromTZData("UTC", data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	_, winter := time.Date(2026, time.January, 1, 0, 0, 0, 0, location).Zone()
	_, summer := time.Date(2026, time.July, 1, 0, 0, 0, 0, location).Zone()
	if winter != 0 || summer != int(time.Hour/time.Second) {
		t.Fatal("季节时区样例未覆盖零偏移与非零偏移转换")
	}
	return location
}
