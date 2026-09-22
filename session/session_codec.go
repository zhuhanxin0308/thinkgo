package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
	"unicode/utf8"
)

// validateSessionKey 校验键名的长度、UTF-8 与控制字符边界。
func validateSessionKey(name string) error {
	if name == "" || len(name) > maxSessionKeyBytes || !utf8.ValidString(name) || hasSessionControl(name) {
		return fmt.Errorf("%w: %q", ErrInvalidSessionKey, name)
	}
	return nil
}

func activeSessionEnvelopeBytes(dataBytes int, config Config, now time.Time) int {
	if config.Expire <= 0 {
		return len(`{"version":1,"data":`) + dataBytes + 1
	}
	expireAt := now.Add(time.Duration(config.Expire) * time.Second).Unix()
	return len(`{"version":1,"expire_at":`) + len(strconv.FormatInt(expireAt, 10)) +
		len(`,"data":`) + dataBytes + 1
}

func sessionDataJSONSize(data map[string]json.RawMessage) int {
	size := 2
	first := true
	for key, value := range data {
		if !first {
			size++
		}
		first = false
		size += sessionJSONKeyBytes(key) + 1 + len(value)
	}
	return size
}

// sessionJSONKeyBytes 精确复现 encoding/json 的字符串转义长度，避免为每次 Set 编码整张 map。
func sessionJSONKeyBytes(value string) int {
	size := 2
	for _, character := range value {
		switch character {
		case '\\', '"', '\b', '\f', '\n', '\r', '\t':
			size += 2
		case '<', '>', '&':
			size += 6
		default:
			if character < 0x20 || character == '\u2028' || character == '\u2029' {
				size += 6
			} else {
				size += utf8.RuneLen(character)
			}
		}
	}
	return size
}

func encodeSessionEnvelope(envelope sessionEnvelope, maxBytes int) (string, bool, error) {
	data, err := json.Marshal(envelope)
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", ErrInvalidSessionValue, err)
	}
	if len(data) > maxBytes {
		return "", false, fmt.Errorf("%w: %d", ErrSessionDataTooLarge, len(data))
	}
	return string(data), false, nil
}

func encodeRevokedEnvelope(config Config, now time.Time) (string, bool, error) {
	lifetime := time.Duration(config.Expire) * time.Second
	if lifetime <= 0 {
		lifetime = 24 * time.Hour
	}
	envelope := sessionEnvelope{
		Version: sessionEnvelopeVersion, ExpireAt: now.Add(lifetime).Unix(), Revoked: true,
		Data: nil,
	}
	return encodeSessionEnvelope(envelope, config.MaxDataBytes)
}

func decodeSessionEnvelope(content string, maxBytes int) (sessionEnvelope, error) {
	if content == "" || len(content) > maxBytes {
		if len(content) > maxBytes {
			return sessionEnvelope{}, fmt.Errorf("%w: %w", ErrCorruptSession, ErrSessionDataTooLarge)
		}
		return sessionEnvelope{}, ErrCorruptSession
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(content)))
	decoder.DisallowUnknownFields()
	var envelope sessionEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return sessionEnvelope{}, fmt.Errorf("%w: %v", ErrCorruptSession, err)
	}
	if err := ensureSessionJSONEOF(decoder); err != nil {
		return sessionEnvelope{}, fmt.Errorf("%w: %v", ErrCorruptSession, err)
	}
	if envelope.Version != sessionEnvelopeVersion || envelope.ExpireAt < 0 {
		return sessionEnvelope{}, ErrCorruptSession
	}
	if envelope.Revoked {
		if len(envelope.Data) != 0 || envelope.ExpireAt == 0 {
			return sessionEnvelope{}, ErrCorruptSession
		}
		return envelope, nil
	}
	if envelope.Data == nil {
		return sessionEnvelope{}, ErrCorruptSession
	}
	for key, value := range envelope.Data {
		if validateSessionKey(key) != nil || len(value) == 0 || !json.Valid(value) {
			return sessionEnvelope{}, ErrCorruptSession
		}
	}
	return envelope, nil
}

func ensureSessionJSONEOF(decoder *json.Decoder) error {
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("包含多余 JSON 值")
		}
		return err
	}
	return nil
}

func decodeSessionValue(raw json.RawMessage) (interface{}, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value interface{}
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := ensureSessionJSONEOF(decoder); err != nil {
		return nil, err
	}
	return normalizeSessionDecodedValue(value), nil
}

// normalizeSessionDecodedValue 恢复 JSON 数字的业务标量类型，避免 Session.Get
// 向业务层泄露编码器内部使用的 json.Number。
func normalizeSessionDecodedValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if decimal, err := typed.Float64(); err == nil {
			return decimal
		}
		return typed.String()
	case []interface{}:
		result := make([]interface{}, len(typed))
		for index, item := range typed {
			result[index] = normalizeSessionDecodedValue(item)
		}
		return result
	case map[string]interface{}:
		result := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			result[key] = normalizeSessionDecodedValue(item)
		}
		return result
	default:
		return value
	}
}
