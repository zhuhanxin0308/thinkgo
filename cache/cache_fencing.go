package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cache/contract"
)

const (
	cacheValueEnvelopeMarker = "thinkgo-cache-value-v1"
	cacheIntegerEncoding     = contract.PreciseIntegerEncodingPrefix
)

var (
	errCacheFenceNoChange          = errors.New("缓存 fencing 无需变更")
	errCacheSequenceBehindEnvelope = errors.New("缓存代际序列落后于已存信封")
)

type cacheValueEnvelope struct {
	Marker     string      `json:"__thinkgo_cache_envelope"`
	Generation string      `json:"generation"`
	Origin     string      `json:"origin,omitempty"`
	Value      interface{} `json:"value"`
	Encoding   string      `json:"encoding,omitempty"`
}

func newCacheValueEnvelope(generation uint64, value interface{}) cacheValueEnvelope {
	encodedValue, encoding := encodePreciseCacheInteger(value)
	return cacheValueEnvelope{
		Marker:     cacheValueEnvelopeMarker,
		Generation: strconv.FormatUint(generation, 10),
		Value:      encodedValue,
		Encoding:   encoding,
	}
}

// newAtomicCacheValueEnvelope 分离唯一提交身份与实际有效基值的来源屏障，重新领票本身不能复活旧值。
func newAtomicCacheValueEnvelope(generation, origin uint64, value interface{}) cacheValueEnvelope {
	envelope := newCacheValueEnvelope(generation, value)
	if origin != generation {
		envelope.Origin = strconv.FormatUint(origin, 10)
	}
	return envelope
}

// encodePreciseCacheInteger 避免 int64、uint64 和 json.Number 经 JSON 后端转成 float64 丢失精度。
func encodePreciseCacheInteger(value interface{}) (interface{}, string) {
	return contract.EncodePreciseCacheInteger(value)
}

func decodePreciseCacheInteger(value interface{}, encoding string) (interface{}, error) {
	return contract.DecodePreciseCacheInteger(value, encoding)
}

// decodeCacheValueEnvelope 同时兼容内存结构、JSON 后端 map 和升级前原始值。
func decodeCacheValueEnvelope(raw interface{}) (interface{}, uint64, bool, error) {
	value, generation, _, enveloped, err := decodeCacheValueFence(raw)
	return value, generation, enveloped, err
}

func decodeCacheValueAtOrigin(raw interface{}) (interface{}, uint64, bool, error) {
	value, _, origin, enveloped, err := decodeCacheValueFence(raw)
	return value, origin, enveloped, err
}

// decodeCacheValueFence 兼容旧信封，将缺省起始代际解释为原有提交代际。
func decodeCacheValueFence(raw interface{}) (interface{}, uint64, uint64, bool, error) {
	var envelope cacheValueEnvelope
	switch typed := raw.(type) {
	case cacheValueEnvelope:
		envelope = typed
	case *cacheValueEnvelope:
		if typed == nil {
			return nil, 0, 0, false, nil
		}
		envelope = *typed
	case map[string]interface{}:
		marker, marked := typed["__thinkgo_cache_envelope"].(string)
		if !marked || marker != cacheValueEnvelopeMarker {
			return raw, 0, 0, false, nil
		}
		generationText, valid := typed["generation"].(string)
		value, hasValue := typed["value"]
		fields := 3
		envelope = cacheValueEnvelope{Marker: marker, Generation: generationText, Value: value}
		for _, optional := range []struct {
			key    string
			target *string
		}{{"encoding", &envelope.Encoding}, {"origin", &envelope.Origin}} {
			if field, exists := typed[optional.key]; exists {
				text, ok := field.(string)
				if !ok || optional.key == "origin" && text == "" {
					return nil, 0, 0, true, ErrCorruptCacheEnvelope
				}
				*optional.target = text
				fields++
			}
		}
		if !valid || !hasValue || len(typed) != fields {
			return nil, 0, 0, true, ErrCorruptCacheEnvelope
		}
	default:
		return raw, 0, 0, false, nil
	}
	generation, err := parseCacheEnvelopeGeneration(envelope.Marker, envelope.Generation)
	if err != nil {
		return nil, 0, 0, true, err
	}
	origin := generation
	if envelope.Origin != "" {
		origin, err = parseCacheEnvelopeGeneration(envelope.Marker, envelope.Origin)
		if err != nil || origin > generation {
			return nil, 0, 0, true, ErrCorruptCacheEnvelope
		}
	}
	value, err := decodePreciseCacheInteger(envelope.Value, envelope.Encoding)
	return value, generation, origin, true, err
}

func parseCacheEnvelopeGeneration(marker, generationText string) (uint64, error) {
	if marker != cacheValueEnvelopeMarker {
		return 0, ErrCorruptCacheEnvelope
	}
	generation, err := strconv.ParseUint(generationText, 10, 64)
	if err != nil || generation == 0 {
		return 0, fmt.Errorf("%w: generation=%q", ErrCorruptCacheEnvelope, generationText)
	}
	return generation, nil
}

func cacheDriverSupportsAtomicFencing(driver Driver) bool {
	if capability, ok := driver.(interface{ supportsAtomicFencing() bool }); ok {
		return capability.supportsAtomicFencing()
	}
	if capability, ok := driver.(interface{ SupportsAtomicFencing() bool }); ok {
		return capability.SupportsAtomicFencing()
	}
	_, atomic := driver.(AtomicUpdater)
	_, preservesTTL := driver.(TTLAtomicUpdater)
	return atomic && preservesTTL
}

func (c *Cache) fenceSequenceKey() string {
	return cacheFencePrefix + cacheMetadataScope + ":sequence"
}

func (c *Cache) fenceInvalidationKey() string {
	return cacheFencePrefix + cacheMetadataScope + ":invalidation"
}

func (c *Cache) nextFenceGeneration(driver Driver) (uint64, error) {
	return c.nextFenceGenerationContext(context.Background(), driver)
}

func (c *Cache) nextFenceGenerationContext(ctx context.Context, driver Driver) (uint64, error) {
	return c.incFenceSequenceContext(ctx, driver, 1)
}

func (c *Cache) incFenceSequenceContext(ctx context.Context, driver Driver, step int64) (uint64, error) {
	if ctx == nil {
		return 0, ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var generation int64
	var err error
	if contextual, ok := driver.(ContextualIncrementer); ok {
		generation, err = contextual.IncContext(ctx, c.fenceSequenceKey(), step)
	} else {
		generation, err = driver.Inc(c.fenceSequenceKey(), step)
	}
	if err != nil {
		return 0, err
	}
	if generation <= 0 {
		return 0, ErrCacheFenceUnavailable
	}
	return uint64(generation), nil
}

func (c *Cache) currentFenceSequenceContext(ctx context.Context, driver Driver) (uint64, error) {
	if allocator, sequence, issued := c.fenceAllocator(driver); allocator != nil {
		return allocator.FenceGenerationFloorContext(ctx, sequence, issued)
	}
	raw, found, err := getCacheValueContext(ctx, driver, c.fenceSequenceKey())
	if err != nil || !found {
		return 0, err
	}
	if f, ok := raw.(float64); ok && f > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return parseFenceGenerationValue(raw)
}

// raiseFenceSequenceFloorContext 在代际序列键被淘汰或重建后原子抬升序列基值，避免后续写入永久报 ErrCacheLockLost。
func (c *Cache) raiseFenceSequenceFloorContext(ctx context.Context, driver Driver, floor uint64) error {
	if floor == 0 {
		return nil
	}
	if floor >= uint64(math.MaxInt64) {
		return ErrCacheFenceUnavailable
	}
	floorValue := int64(floor)
	err := atomicUpdateCacheValueContext(ctx, driver, c.fenceSequenceKey(), 0, func(raw interface{}, found bool) (interface{}, bool, error) {
		if found {
			if f, ok := raw.(float64); ok && f >= float64(floor) {
				return nil, false, errCacheFenceNoChange
			}
			if current, parseErr := parseFenceGenerationValue(raw); parseErr == nil && current >= floor {
				return nil, false, errCacheFenceNoChange
			}
		}
		return floorValue, false, nil
	})
	if errors.Is(err, errCacheFenceNoChange) {
		return nil
	}
	return err
}

// allocateFenceGenerationAboveContext 分配严格大于 floor 的单调代际；若序列回退则原子自愈后重试。
func (c *Cache) allocateFenceGenerationAboveContext(ctx context.Context, driver Driver, floor uint64) (uint64, error) {
	return c.allocateFenceGenerationWithWitness(ctx, driver, floor, 0)
}

func (c *Cache) allocateFenceGenerationWithWitness(ctx context.Context, driver Driver, floor, minimumIssued uint64) (uint64, error) {
	if allocator, sequence, issued := c.fenceAllocator(driver); allocator != nil {
		return allocator.AllocateFenceGenerationContext(ctx, sequence, issued, floor, minimumIssued)
	}
	for attempt := 0; attempt < maxCacheAtomicRebaseAttempts; attempt++ {
		generation, err := c.nextFenceGenerationContext(ctx, driver)
		if err != nil {
			return 0, err
		}
		if generation > floor {
			return generation, nil
		}
		if err := c.raiseFenceSequenceFloorContext(ctx, driver, floor); err != nil {
			return 0, err
		}
	}
	return 0, ErrCacheFenceUnavailable
}

func (c *Cache) currentInvalidationGeneration(ctx context.Context, driver Driver) (uint64, error) {
	raw, found, err := getCacheValueContext(ctx, driver, c.fenceInvalidationKey())
	if err != nil || !found {
		return 0, err
	}
	return parseFenceGenerationValue(raw)
}

func parseFenceGenerationValue(raw interface{}) (uint64, error) {
	switch typed := raw.(type) {
	case string:
		parsed, err := strconv.ParseUint(typed, 10, 64)
		if err == nil && parsed > 0 {
			return parsed, nil
		}
	case int:
		if typed > 0 {
			return uint64(typed), nil
		}
	case int64:
		if typed > 0 {
			return uint64(typed), nil
		}
	case float64:
		if typed > 0 && typed <= math.MaxInt64 && typed == math.Trunc(typed) {
			return uint64(typed), nil
		}
	case json.Number:
		parsed, parseErr := strconv.ParseUint(typed.String(), 10, 64)
		if parseErr == nil && parsed > 0 {
			return parsed, nil
		}
	}
	return 0, ErrCacheFenceUnavailable
}

// beginFenceInvalidationContext 先发布单调失效代际，再允许删除旧值；序列回退时基于已有全局水位自愈。
func (c *Cache) beginFenceInvalidationContext(ctx context.Context, driver Driver, floor uint64) (uint64, error) {
	minimumIssued := floor
	for attempt := 0; attempt < maxCacheAtomicRebaseAttempts; attempt++ {
		generation, err := c.allocateFenceGenerationWithWitness(ctx, driver, floor, minimumIssued)
		if err != nil {
			return 0, err
		}
		var observed uint64
		err = atomicUpdateCacheValueContext(ctx, driver, c.fenceInvalidationKey(), 0, func(raw interface{}, found bool) (interface{}, bool, error) {
			if found {
				current, parseErr := parseFenceGenerationValue(raw)
				if parseErr != nil {
					return nil, false, parseErr
				}
				if current >= generation {
					observed = current
					return nil, false, errCacheSequenceBehindEnvelope
				}
			}
			return strconv.FormatUint(generation, 10), false, nil
		})
		if errors.Is(err, errCacheSequenceBehindEnvelope) {
			currentSeq, seqErr := c.currentFenceSequenceContext(ctx, driver)
			if seqErr != nil {
				return 0, seqErr
			}
			if currentSeq >= observed && observed > generation {
				// 另一并发全局清理已推进更高水位，无需覆盖，但返回实际生效的本轮栅栏。
				return generation, nil
			}
			floor = max(floor, observed)
			continue
		}
		return generation, err
	}
	return 0, ErrCacheFenceUnavailable
}

func atomicUpdateCacheValueContext(ctx context.Context, driver Driver, key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	if ctx == nil {
		return ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if contextual, ok := driver.(ContextualAtomicUpdater); ok {
		return contextual.UpdateContext(ctx, key, ttl, update)
	}
	updater, ok := driver.(AtomicUpdater)
	if !ok {
		return ErrCacheAtomicUpdateUnsupported
	}
	return updater.Update(key, ttl, update)
}

func atomicUpdateCacheValuePreserveTTL(driver Driver, key string, update func(interface{}, bool) (interface{}, bool, error)) error {
	updater, ok := driver.(TTLAtomicUpdater)
	if !ok {
		return ErrCacheAtomicUpdateUnsupported
	}
	return updater.UpdatePreserveTTL(key, update)
}

// atomicUpdateCacheValueConditionalTTL 在原子回调内决定 TTL；旧驱动只能处理不需要清除现有 TTL 的分支。
func atomicUpdateCacheValueConditionalTTL(driver Driver, key string, update func(interface{}, bool) (interface{}, bool, bool, error)) error {
	if update == nil {
		return ErrNilCacheUpdate
	}
	if updater, ok := driver.(ConditionalTTLAtomicUpdater); ok {
		return updater.UpdatePreserveTTLConditionally(key, update)
	}
	return atomicUpdateCacheValuePreserveTTL(driver, key, func(raw interface{}, found bool) (interface{}, bool, error) {
		next, remove, preserveTTL, err := update(raw, found)
		if err == nil && found && !remove && !preserveTTL {
			return nil, false, ErrCacheAtomicUpdateUnsupported
		}
		return next, remove, err
	})
}

func (c *Cache) writeFencedCacheValueContext(ctx context.Context, driver Driver, key string, value interface{}, ttl time.Duration) error {
	if err := validateCacheNumber(value); err != nil {
		return err
	}
	if !cacheDriverSupportsAtomicFencing(driver) {
		return setCacheValueContext(ctx, driver, key, value, ttl)
	}
	floor, err := c.invalidationForKey(ctx, driver, key)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < maxCacheAtomicRebaseAttempts; attempt++ {
		generation, err := c.allocateFenceGenerationAboveContext(ctx, driver, floor)
		if err != nil {
			return err
		}
		preCommit, preErr := c.invalidationForKey(ctx, driver, key)
		if preErr != nil {
			return preErr
		}
		if preCommit >= generation {
			return ErrCacheLockLost
		}
		observed, commitErr := c.commitFencedValueObservedContext(ctx, driver, key, value, ttl, generation)
		if errors.Is(commitErr, errCacheSequenceBehindEnvelope) {
			currentSeq, seqErr := c.currentFenceSequenceContext(ctx, driver)
			if seqErr != nil {
				return seqErr
			}
			if observed > generation && currentSeq >= observed {
				return ErrCacheLockLost
			}
			floor = max(floor, observed)
			continue
		}
		if commitErr != nil {
			return commitErr
		}
		return c.verifyFenceCommitContext(ctx, driver, key, generation)
	}
	return ErrCacheFenceUnavailable
}

func (c *Cache) commitFencedValueContext(ctx context.Context, driver Driver, key string, value interface{}, ttl time.Duration, generation uint64) error {
	_, err := c.commitFencedValueObservedContext(ctx, driver, key, value, ttl, generation)
	if errors.Is(err, errCacheSequenceBehindEnvelope) {
		return ErrCacheLockLost
	}
	return err
}

func (c *Cache) commitFencedValueObservedContext(ctx context.Context, driver Driver, key string, value interface{}, ttl time.Duration, generation uint64) (uint64, error) {
	if err := validateCacheNumber(value); err != nil {
		return 0, err
	}
	var observed uint64
	err := atomicUpdateCacheValueContext(ctx, driver, key, ttl, func(raw interface{}, found bool) (interface{}, bool, error) {
		if found {
			_, currentGeneration, enveloped, err := decodeCacheValueEnvelope(raw)
			if err != nil {
				return nil, false, err
			}
			if enveloped && currentGeneration >= generation {
				observed = currentGeneration
				return nil, false, errCacheSequenceBehindEnvelope
			}
		}
		return newCacheValueEnvelope(generation, value), false, nil
	})
	return observed, err
}

func (c *Cache) writeFencedCacheValuesContext(ctx context.Context, driver Driver, keys []string, values map[string]interface{}, ttl time.Duration) error {
	if len(keys) == 0 {
		return nil
	}
	updater, ok := driver.(ExpiryAtomicUpdater)
	if !ok {
		return ErrCacheAtomicUpdateUnsupported
	}
	keyInvalidations := make(map[string]uint64, len(keys))
	var floor uint64
	for _, key := range keys {
		invalidation, err := c.invalidationForKey(ctx, driver, key)
		if err != nil {
			return err
		}
		keyInvalidations[key] = invalidation
		floor = max(floor, invalidation)
	}
	for attempt := 0; attempt < maxCacheAtomicRebaseAttempts; attempt++ {
		generation, err := c.allocateFenceGenerationAboveContext(ctx, driver, floor)
		if err != nil {
			return err
		}
		committed := make([]cacheBatchSnapshot, 0, len(keys))
		var observed uint64
		for _, key := range keys {
			preCommit, preErr := c.invalidationForKey(ctx, driver, key)
			if preErr != nil {
				err = preErr
				break
			}
			if preCommit >= generation {
				err = ErrCacheLockLost
				break
			}
			invalidation := keyInvalidations[key]
			snapshot := cacheBatchSnapshot{key: key}
			err = updater.UpdateWithExpiryContext(ctx, key, ttl, func(raw interface{}, found bool, expiry time.Time) (interface{}, bool, *time.Time, error) {
				snapshot = cacheBatchSnapshot{key: key}
				if found {
					_, current, prevOrigin, enveloped, decodeErr := decodeCacheValueFence(raw)
					if decodeErr != nil {
						return nil, false, nil, decodeErr
					}
					if enveloped && current >= generation {
						observed = current
						return nil, false, nil, errCacheSequenceBehindEnvelope
					}
					if invalidation > 0 && prevOrigin <= invalidation {
						// 旧物理项已被失效水位屏蔽，回滚时必须删除，不得借新批次代际复活脏值。
						found = false
						raw = nil
					}
				}
				snapshot = cacheBatchSnapshot{key: key, value: raw, found: found, expiry: expiry, captured: true}
				return newCacheValueEnvelope(generation, values[key]), false, nil, nil
			})
			// 即使响应丢失，回调已执行的键也可能实际提交；条件回滚会核验真正的代际。
			if snapshot.captured {
				committed = append(committed, snapshot)
			}
			if err != nil {
				break
			}
		}
		if errors.Is(err, errCacheSequenceBehindEnvelope) {
			rollbackErr := c.rollbackCommittedBatchContext(ctx, driver, updater, committed, generation)
			currentSeq, seqErr := c.currentFenceSequenceContext(ctx, driver)
			if seqErr != nil {
				return errors.Join(seqErr, rollbackErr)
			}
			if observed > generation && currentSeq >= observed {
				return errors.Join(ErrCacheLockLost, rollbackErr)
			}
			if rollbackErr != nil {
				return rollbackErr
			}
			floor = max(floor, observed)
			continue
		}
		if err == nil {
			detached := context.WithoutCancel(ctx)
			for _, snapshot := range committed {
				latestInvalidation, fenceErr := c.invalidationForKey(detached, driver, snapshot.key)
				if fenceErr != nil {
					err = fenceErr
					break
				}
				if latestInvalidation > generation {
					err = ErrCacheLockLost
					break
				}
			}
		}
		if err == nil {
			return nil
		}
		return errors.Join(err, c.rollbackCommittedBatchContext(ctx, driver, updater, committed, generation))
	}
	return ErrCacheFenceUnavailable
}

func (c *Cache) rollbackCommittedBatchContext(ctx context.Context, driver Driver, updater ExpiryAtomicUpdater, committed []cacheBatchSnapshot, generation uint64) error {
	detached := context.WithoutCancel(ctx)
	var resultErr error
	for index := len(committed) - 1; index >= 0; index-- {
		snapshot := committed[index]
		if snapshot.found {
			if latestInvalidation, invErr := c.invalidationForKey(detached, driver, snapshot.key); invErr == nil && latestInvalidation > 0 {
				if _, _, prevOrigin, _, decErr := decodeCacheValueFence(snapshot.value); decErr == nil && prevOrigin <= latestInvalidation {
					snapshot.found = false
					snapshot.value = nil
				}
			}
		}
		resultErr = errors.Join(resultErr, rollbackCacheBatchSnapshot(detached, updater, snapshot, generation))
	}
	return resultErr
}

func (c *Cache) verifyFenceCommitContext(ctx context.Context, driver Driver, key string, generation uint64) error {
	return c.verifyAtomicFenceCommitContext(ctx, driver, key, generation, generation)
}

func (c *Cache) verifyAtomicFenceCommitContext(ctx context.Context, driver Driver, key string, generation, origin uint64) error {
	detached := context.WithoutCancel(ctx)
	current, err := c.invalidationForKey(detached, driver, key)
	if err != nil {
		return err
	}
	if current <= origin {
		return nil
	}
	cleanupErr := c.deleteExactGenerationContext(detached, driver, key, generation)
	return errors.Join(ErrCacheLockLost, cleanupErr)
}

func (c *Cache) deleteExactGenerationContext(ctx context.Context, driver Driver, key string, generation uint64) error {
	err := atomicUpdateCacheValueContext(ctx, driver, key, 0, func(raw interface{}, found bool) (interface{}, bool, error) {
		if !found {
			return nil, false, errCacheFenceNoChange
		}
		_, currentGeneration, enveloped, decodeErr := decodeCacheValueEnvelope(raw)
		if decodeErr != nil {
			return nil, false, decodeErr
		}
		if !enveloped || currentGeneration != generation {
			return nil, false, errCacheFenceNoChange
		}
		return nil, true, nil
	})
	if errors.Is(err, errCacheFenceNoChange) {
		return nil
	}
	return err
}

// deleteAtFenceContext 只删除不晚于失效代际的值；更新后的新一代值保持不变。
func (c *Cache) deleteAtFenceContext(ctx context.Context, driver Driver, key string, fence uint64) (bool, error) {
	if !cacheDriverSupportsAtomicFencing(driver) {
		return true, deleteCacheValueContext(ctx, driver, key)
	}
	deleted := false
	err := atomicUpdateCacheValueContext(ctx, driver, key, 0, func(raw interface{}, found bool) (interface{}, bool, error) {
		if !found {
			return nil, false, errCacheFenceNoChange
		}
		_, generation, _, decodeErr := decodeCacheValueAtOrigin(raw)
		if decodeErr != nil {
			return nil, false, decodeErr
		}
		if generation > fence {
			return nil, false, errCacheFenceNoChange
		}
		deleted = true
		return nil, true, nil
	})
	if errors.Is(err, errCacheFenceNoChange) {
		return false, nil
	}
	return deleted, err
}
