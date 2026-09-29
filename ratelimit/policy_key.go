package ratelimit

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// PolicyKey 将已经有业务命名空间的客户端键与限流策略绑定。
// 同一策略跨进程生成相同键，不同策略不会相互重置额度。输入先沿用 Store 的
// 校验与规范化，再摘要以保持物理键有界。中间件已自动调用此函数；管理员
// 直接调用 Store.Reset 时也应传入此函数的结果，而不是原始 IP/账号。
// 相同速率策略需要独立额度时，调用方仍须在原始键中包含稳定业务标识。
func PolicyKey(key string, limit Limit) (string, error) {
	key, err := normalizeKey(key)
	if err != nil {
		return "", err
	}
	if _, err := Validate(limit); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(key))
	return "policy:v1:" + strconv.Itoa(limit.Rate) + ":" +
		strconv.FormatInt(int64(limit.Period), 10) + ":" +
		strconv.Itoa(limit.Burst) + ":" + hex.EncodeToString(digest[:]), nil
}
