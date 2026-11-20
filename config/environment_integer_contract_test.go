package config

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestEnvironmentIntegerPrototypeUsesExactValue 验证数学上相同的整数声明具有相同覆盖规则。
func TestEnvironmentIntegerPrototypeUsesExactValue(t *testing.T) {
	for _, prototype := range []json.Number{"10", "10.0", "1e3", "100e-2", "9223372036854775808", "1e1000001"} {
		t.Run(prototype.String(), func(t *testing.T) {
			configuration := NewConfig()
			if err := configuration.Set("feature.limit", prototype); err != nil {
				t.Fatal(err)
			}
			if err := configuration.Set("app.debug", false); err != nil {
				t.Fatal(err)
			}
			err := configuration.ApplyEnvironment(environmentStub{"APP_DEBUG": "true", "FEATURE_LIMIT": "1.5"})
			if !errors.Is(err, ErrConfigEnvironmentType) {
				t.Fatalf("整数原型必须拒绝小数覆盖: %v", err)
			}
			if configuration.Get("feature.limit") != prototype || configuration.GetBool("app.debug") {
				t.Fatal("整数类型错误必须原子回滚全部覆盖")
			}
		})
	}
}

// TestEnvironmentIntegerOverrideAcceptsEquivalentNotation 验证整数覆盖支持精确指数和小数记法。
func TestEnvironmentIntegerOverrideAcceptsEquivalentNotation(t *testing.T) {
	for _, raw := range []string{"20", "20.0", "2e1", "200e-1"} {
		t.Run(raw, func(t *testing.T) {
			configuration := NewConfig()
			if err := configuration.Set("feature.limit", json.Number("10")); err != nil {
				t.Fatal(err)
			}
			if err := configuration.ApplyEnvironment(environmentStub{"FEATURE_LIMIT": raw}); err != nil {
				t.Fatal(err)
			}
			if value, err := configuration.GetIntStrict("feature.limit"); err != nil || value != 20 {
				t.Fatalf("覆盖后的整数不可读取: value=%d err=%v", value, err)
			}
		})
	}
}

// TestEnvironmentIntegerRangeAndFractionalPrototype 验证范围错误不会退化为浮点配置。
func TestEnvironmentIntegerRangeAndFractionalPrototype(t *testing.T) {
	for _, raw := range []string{"9223372036854775808", "-9223372036854775809", "1e100", "1e-1"} {
		if _, err := parseConfigEnvironmentNumber("feature.limit", raw, json.Number("1e3")); !errors.Is(err, ErrConfigEnvironmentType) {
			t.Errorf("整数覆盖 %q 应被拒绝: %v", raw, err)
		}
	}
	for _, prototype := range []json.Number{"1.25", "1e-1"} {
		if actual, err := parseConfigEnvironmentNumber("feature.ratio", "2.5", prototype); err != nil || actual != "2.5" {
			t.Errorf("小数原型不应丢失小数覆盖能力: value=%s err=%v", actual, err)
		}
	}
	if _, err := parseConfigEnvironmentNumber("feature.ratio", "1.5", json.Number("1e-1000001")); !errors.Is(err, ErrConfigEnvironmentType) {
		t.Errorf("超出精确解析范围的原型必须明确报错，不能假定为小数: %v", err)
	}
}

// TestEnvironmentNumberDoesNotPublishUnparseablePrototype 验证一次覆盖不能使后续合法覆盖永久失败。
func TestEnvironmentNumberDoesNotPublishUnparseablePrototype(t *testing.T) {
	configuration := NewConfig()
	const original = json.Number("1.25")
	if err := configuration.Set("feature.ratio", original); err != nil {
		t.Fatal(err)
	}
	if err := configuration.ApplyEnvironment(environmentStub{"FEATURE_RATIO": "1e1000001"}); !errors.Is(err, ErrConfigEnvironmentType) {
		t.Fatalf("无法精确解析的新值应在发布前被拒绝: %v", err)
	}
	if configuration.Get("feature.ratio") != original {
		t.Fatal("失败覆盖不得更换数值原型")
	}
	if err := configuration.ApplyEnvironment(environmentStub{"FEATURE_RATIO": "2.5"}); err != nil {
		t.Fatalf("失败后必须还能接受后续合法覆盖: %v", err)
	}
	if actual := configuration.Get("feature.ratio"); actual != json.Number("2.5") {
		t.Fatalf("后续覆盖结果错误: %#v", actual)
	}
}
