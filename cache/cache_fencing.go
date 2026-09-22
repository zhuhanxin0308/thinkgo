package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	cacheValueEnvelopeMarker = "thinkgo-cache-value-v1"
	cacheIntegerEncoding     = "integer:"
)

var errCacheFenceNoChange = errors.New("缓存 fencing 无需变更")

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
	switch typed := value.(type) {
	case int:
		if int64(typed) > 1<<53 || int64(typed) < -(1<<53) {
			return strconv.FormatInt(int64(typed), 10), cacheIntegerEncoding + "int"
		}
	case int64:
		return strconv.FormatInt(typed, 10), cacheIntegerEncoding + "int64"
	case uint:
		if uint64(typed) > 1<<53 {
			return strconv.FormatUint(uint64(typed), 10), cacheIntegerEncoding + "uint"
		}
	case uint64:
		return strconv.FormatUint(typed, 10), cacheIntegerEncoding + "uint64"
	case json.Number:
		if _, err := strconv.ParseInt(typed.String(), 10, 64); err == nil {
			return typed.String(), cacheIntegerEncoding + "json-number"
		}
	}
	return value, ""
}

func decodePreciseCacheInteger(value interface{}, encoding string) (interface{}, error) {
	if encoding == "" {
		return value, nil
	}
	text, ok := value.(string)
	if !ok || !strings.HasPrefix(encoding, cacheIntegerEncoding) {
		return nil, ErrCorruptCacheEnvelope
	}
	kind := strings.TrimPrefix(encoding, cacheIntegerEncoding)
	switch kind {
	case "int", "int64", "json-number":
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, ErrCorruptCacheEnvelope
		}
		switch kind {
		case "int":
			if strconv.IntSize == 32 && (parsed < math.MinInt32 || parsed > math.MaxInt32) {
				return nil, ErrCorruptCacheEnvelope
			}
			return int(parsed), nil
		case "json-number":
			return json.Number(text), nil
		default:
			return parsed, nil
		}
	case "uint", "uint64":
		parsed, err := strconv.ParseUint(text, 10, 64)
		if err != nil || kind == "uint" && strconv.IntSize == 32 && parsed > math.MaxUint32 {
			return nil, ErrCorruptCacheEnvelope
		}
		if kind == "uint" {
			return uint(parsed), nil
		}
		return parsed, nil
	default:
		return nil, ErrCorruptCacheEnvelope
	}
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
	_, atomic := driver.(AtomicUpdater)
	_, preservesTTL := driver.(TTLAtomicUpdater)
	return atomic && preservesTTL
}

func (c *Cache) fenceSequenceKey() string {
	return cacheFencePrefix + c.storeName + ":sequence"
}

func (c *Cache) fenceInvalidationKey() string {
	return cacheFencePrefix + c.storeName + ":invalidation"
}

func (c *Cache) nextFenceGeneration(driver Driver) (uint64, error) {
	return c.nextFenceGenerationContext(context.Background(), driver)
}

func (c *Cache) nextFenceGenerationContext(ctx context.Context, driver Driver) (uint64, error) {
	if ctx == nil {
		return 0, ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var generation int64
	var err error
	if contextual, ok := driver.(ContextualIncrementer); ok {
		generation, err = contextual.IncContext(ctx, c.fenceSequenceKey(), 1)
	} else {
		generation, err = driver.Inc(c.fenceSequenceKey(), 1)
	}
	if err != nil {
		return 0, err
	}
	if generation <= 0 {
		return 0, ErrCacheFenceUnavailable
	}
	return uint64(generation), nil
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

// beginFenceInvalidationContext 先发布单调失效代际，再允许删除旧值。
func (c *Cache) beginFenceInvalidationContext(ctx context.Context, driver Driver) (uint64, error) {
	generation, err := c.nextFenceGenerationContext(ctx, driver)
	if err != nil {
		return 0, err
	}
	err = atomicUpdateCacheValueContext(ctx, driver, c.fenceInvalidationKey(), 0, func(raw interface{}, found bool) (interface{}, bool, error) {
		if found {
			current, parseErr := parseFenceGenerationValue(raw)
			if parseErr != nil {
				return nil, false, parseErr
			}
			if current >= generation {
				return raw, false, nil
			}
		}
		return strconv.FormatUint(generation, 10), false, nil
	})
	return generation, err
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
	if !cacheDriverSupportsAtomicFencing(driver) {
		return setCacheValueContext(ctx, driver, key, value, ttl)
	}
	generation, err := c.nextFenceGenerationContext(ctx, driver)
	if err != nil {
		return err
	}
	if err = c.commitFencedValueContext(ctx, driver, key, value, ttl, generation); err != nil {
		return err
	}
	return c.verifyFenceCommitContext(ctx, driver, key, generation)
}

func (c *Cache) commitFencedValueContext(ctx context.Context, driver Driver, key string, value interface{}, ttl time.Duration, generation uint64) error {
	return atomicUpdateCacheValueContext(ctx, driver, key, ttl, func(raw interface{}, found bool) (interface{}, bool, error) {
		if found {
			_, currentGeneration, enveloped, err := decodeCacheValueEnvelope(raw)
			if err != nil {
				return nil, false, err
			}
			if enveloped && currentGeneration > generation {
				return nil, false, ErrCacheLockLost
			}
		}
		return newCacheValueEnvelope(generation, value), false, nil
	})
}

func (c *Cache) writeFencedCacheValuesContext(ctx context.Context, driver Driver, keys []string, values map[string]interface{}, ttl time.Duration) error {
	if len(keys) == 0 {
		return nil
	}
	generation, err := c.nextFenceGenerationContext(ctx, driver)
	if err != nil {
		return err
	}
	committed := make([]string, 0, len(keys))
	for _, key := range keys {
		if err = c.commitFencedValueContext(ctx, driver, key, values[key], ttl, generation); err != nil {
			break
		}
		committed = append(committed, key)
	}
	if err == nil {
		for _, key := range committed {
			latestInvalidation, fenceErr := c.invalidationForKey(ctx, driver, key)
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
	for _, key := range committed {
		err = errors.Join(err, c.deleteExactGenerationContext(context.WithoutCancel(ctx), driver, key, generation))
	}
	return err
}

func (c *Cache) verifyFenceCommitContext(ctx context.Context, driver Driver, key string, generation uint64) error {
	return c.verifyAtomicFenceCommitContext(ctx, driver, key, generation, generation)
}

func (c *Cache) verifyAtomicFenceCommitContext(ctx context.Context, driver Driver, key string, generation, origin uint64) error {
	current, err := c.invalidationForKey(ctx, driver, key)
	if err != nil {
		return err
	}
	if current <= origin {
		return nil
	}
	cleanupErr := c.deleteExactGenerationContext(context.WithoutCancel(ctx), driver, key, generation)
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
