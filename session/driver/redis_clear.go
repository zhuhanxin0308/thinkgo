package driver

import (
	"context"
	"errors"
	"strings"

	"github.com/redis/go-redis/v9"
)

const (
	redisSessionGenerationChanged  = -2
	redisSessionClearBatchSize     = 256
	redisSessionClearTimeoutFactor = 5
	redisSessionClearingPrefix     = "clearing:"
	redisSessionReadyPrefix        = "ready:"
	redisSessionClearDeleteScript  = `
if redis.call("GET", KEYS[1]) ~= ARGV[1] then
	return -2
end
for i = 2, #KEYS do
	redis.call("DEL", KEYS[i])
end
return 1
`
	redisSessionClearFinishScript = `
if redis.call("GET", KEYS[1]) ~= ARGV[1] then
	return -2
end
redis.call("SET", KEYS[1], ARGV[2])
return 1
`
)

// generationKey 使用包含分隔符的元数据后缀，不能被嵌套前缀的合法 Session ID 清理匹配。
func (r *Redis) generationKey() string { return r.prefix + "meta:generation" }

func (r *Redis) readGeneration(parent context.Context) (string, error) {
	ctx, cancel := r.contextWithParent(parent)
	defer cancel()
	generation, err := r.client.Get(ctx, r.generationKey()).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(generation, redisSessionClearingPrefix) {
		return "", ErrSessionCleared
	}
	return generation, nil
}

// Clear 用命名空间代际阻止清理前的提交，并只删除本命名空间合法数据键。
// 清理期间新操作明确失败；失败清理可能已删除部分键，但不会重新允许旧代际提交。
func (r *Redis) Clear() (returnErr error) {
	if err := r.validateClient(); err != nil {
		return err
	}
	owner, err := redisSessionOwner()
	if err != nil {
		return err
	}
	clearing, ready := redisSessionClearingPrefix+owner, redisSessionReadyPrefix+owner
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout*redisSessionClearTimeoutFactor)
	defer cancel()
	if err := r.client.Set(ctx, r.generationKey(), clearing, 0).Err(); err != nil {
		return err
	}
	defer func() {
		// 使用独立有界上下文结束本代际，原扫描超时也不会永久锁住新会话。
		finish, stop := r.context()
		defer stop()
		result, err := r.client.Eval(finish, redisSessionClearFinishScript, []string{r.generationKey()}, clearing, ready).Result()
		if err == nil && result != int64(1) {
			err = ErrSessionCleared
		}
		returnErr = errors.Join(returnErr, err)
	}()
	var cursor uint64
	for {
		keys, next, err := r.client.Scan(ctx, cursor, r.prefix+redisSessionDataPrefix+"*", redisSessionClearBatchSize).Result()
		if err != nil {
			return err
		}
		owned := []string{r.generationKey()}
		for _, key := range keys {
			id, matches := strings.CutPrefix(key, r.prefix+redisSessionDataPrefix)
			if matches && validateSessionID(id) == nil {
				owned = append(owned, key)
			}
		}
		// 即使并行 Clear 接管，也必须先在同一脚本核对代际，不能删掉后继清理后的新数据。
		result, err := r.client.Eval(ctx, redisSessionClearDeleteScript, owned, clearing).Result()
		if err != nil {
			return err
		}
		if result != int64(1) {
			return ErrSessionCleared
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}
