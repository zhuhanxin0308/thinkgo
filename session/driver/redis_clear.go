package driver

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	redisSessionGenerationChanged    = -2
	redisSessionClearBatchSize       = 256
	redisSessionClearTimeoutFactor   = 5
	redisSessionClearingPrefix       = "clearing:"
	redisSessionReadyPrefix          = "ready:"
	redisSessionReadGenerationScript = `
local generation = redis.call("GET", KEYS[1]) or ""
if redis.call("GET", KEYS[2]) == generation then
	return -2
end
if string.sub(generation, 1, string.len(ARGV[1])) == ARGV[1] then
	generation = ARGV[2] .. string.sub(generation, string.len(ARGV[1]) + 1)
	redis.call("SET", KEYS[1], generation)
end
return generation
`
	redisSessionClearStartScript = `
redis.call("SET", KEYS[1], ARGV[1])
redis.call("SET", KEYS[2], ARGV[1], "PX", ARGV[2])
return 1
`
	redisSessionClearDeleteScript = `
if redis.call("GET", KEYS[1]) ~= ARGV[1] or redis.call("GET", KEYS[2]) ~= ARGV[1] then
	return -2
end
for i = 3, #KEYS do
	redis.call("DEL", KEYS[i])
end
return 1
`
	redisSessionClearFinishScript = `
if redis.call("GET", KEYS[1]) ~= ARGV[1] or redis.call("GET", KEYS[2]) ~= ARGV[1] then
	return -2
end
redis.call("SET", KEYS[1], ARGV[2])
redis.call("DEL", KEYS[2])
return 1
`
)

// generationKey 使用包含分隔符的元数据后缀，不能被嵌套前缀的合法 Session ID 清理匹配。
func (r *Redis) generationKey() string { return r.prefix + "meta:generation" }

func (r *Redis) clearLeaseKey() string { return r.prefix + "meta:clearing" }

func (r *Redis) readGeneration(parent context.Context) (string, error) {
	ctx, cancel := r.contextWithParent(parent)
	defer cancel()
	// 活动租约有期限，代际始终持久；租约失效时原子结束遗留状态，绝不回到旧代际。
	result, err := r.client.Eval(ctx, redisSessionReadGenerationScript,
		[]string{r.generationKey(), r.clearLeaseKey()}, redisSessionClearingPrefix, redisSessionReadyPrefix).Result()
	if err != nil {
		return "", err
	}
	if result == int64(redisSessionGenerationChanged) {
		return "", ErrSessionCleared
	}
	generation, ok := result.(string)
	if !ok {
		return "", fmt.Errorf("Redis Session 清理代际返回类型错误: %T", result)
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
	clearBudget := r.timeout * redisSessionClearTimeoutFactor
	ctx, cancel := context.WithTimeout(context.Background(), clearBudget)
	defer cancel()
	if err := r.client.Eval(ctx, redisSessionClearStartScript,
		[]string{r.generationKey(), r.clearLeaseKey()}, clearing, clearBudget.Milliseconds()).Err(); err != nil {
		return err
	}
	defer func() {
		// 正常结束立即解除租约；进程退出或网络故障时，由到期后的首次操作原子恢复。
		finish, stop := r.context()
		defer stop()
		result, err := r.client.Eval(finish, redisSessionClearFinishScript, []string{r.generationKey(), r.clearLeaseKey()}, clearing, ready).Result()
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
		owned := []string{r.generationKey(), r.clearLeaseKey()}
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
