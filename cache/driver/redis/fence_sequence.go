package redis

import (
	"context"
	"math"
	"strconv"

	"github.com/redis/go-redis/v9"
	"github.com/zhuhanxin0308/thinkgo/v3/cache/contract"
)

// Redis Lua 数值只精确到 53 位。按十进制串长度和字典序比较，并从末位进位，
// 在唯一一次 MSET 中同时写入旧序列与已发票号，避免进程中断或单键淘汰导致重发。
var allocateFenceSequence = redis.NewScript(`
local floor = '0'
local limit = ARGV[2]
local function less(left, right)
	return #left < #right or (#left == #right and left < right)
end
for i = 1, #KEYS do
	local current = redis.call('GET', KEYS[i])
	if current then
		if not string.match(current, '^[1-9][0-9]*$') and current ~= '0' then
			return ''
		end
		if less(floor, current) then
			floor = current
		end
	end
end
if less(limit, floor) or less(floor, ARGV[4]) then
	return ''
end
if ARGV[3] == 'read' then
	return floor
end
if less(floor, ARGV[1]) then
	floor = ARGV[1]
end
if floor == limit then
	return ''
end
local digit = #floor
while digit > 0 and string.sub(floor, digit, digit) == '9' do
	digit = digit - 1
end
local prefix = '1'
if digit > 0 then
	prefix = string.sub(floor, 1, digit - 1) .. string.char(string.byte(floor, digit) + 1)
end
local next = prefix .. string.rep('0', #floor - digit)
redis.call('MSET', KEYS[1], next, KEYS[2], next)
return next
`)

// AllocateFenceGenerationContext 在一个 Redis 原子操作内兼容既有序列，并保留相同命名空间的已发票号。
func (c *Redis) AllocateFenceGenerationContext(parent context.Context, sequence, issued string, floor, minimumIssued uint64) (uint64, error) {
	return c.fenceGenerationContext(parent, sequence, issued, floor, minimumIssued, "allocate")
}

// FenceGenerationFloorContext 无损读取旧序列与已发票号的最高值，两项均缺失时返回零。
func (c *Redis) FenceGenerationFloorContext(parent context.Context, sequence, issued string) (uint64, error) {
	return c.fenceGenerationContext(parent, sequence, issued, 0, 0, "read")
}

func (c *Redis) fenceGenerationContext(parent context.Context, sequence, issued string, floor, minimumIssued uint64, operation string) (uint64, error) {
	if parent == nil {
		return 0, ErrInvalidRedisContext
	}
	if err := c.validateClient(); err != nil {
		return 0, err
	}
	if sequence == "" || issued == "" || sequence == issued || floor >= math.MaxInt64 {
		return 0, contract.ErrCacheFenceUnavailable
	}
	ctx, cancel := c.contextWithParent(parent)
	defer cancel()
	keys := []string{c.withPrefix(sequence), c.withPrefix(issued)}
	next, err := allocateFenceSequence.Run(ctx, c.client, keys, strconv.FormatUint(floor, 10), strconv.FormatInt(math.MaxInt64, 10), operation, strconv.FormatUint(minimumIssued, 10)).Text()
	if err != nil {
		return 0, err
	}
	generation, err := strconv.ParseUint(next, 10, 63)
	if err != nil {
		return 0, contract.ErrCacheFenceUnavailable
	}
	return generation, nil
}
