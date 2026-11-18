package log

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// TestErrorTextCredentialPrefixes 仅保留长凭据开头四个字符，不暴露短凭据或后缀。
func TestErrorTextCredentialPrefixes(t *testing.T) {
	for _, testCase := range []struct {
		name, input, want string
	}{
		{"普通密码", "password=abcdefgh host=db", "password=abcd[REDACTED] host=db"},
		{"分隔符开头的密码", "password=;hunter2 status=failed", "password=[REDACTED] status=failed"},
		{"分隔符在密码内部", "password=abcd;tail,more&end safe=yes", "password=[REDACTED] safe=yes"},
		{"分隔符不计入短值长度", "password=abcd, status=failed", "password=[REDACTED] status=failed"},
		{"兼容复合键", "user_password=abcdefgh someToken=ijklmnop", "user_password=abcd[REDACTED] someToken=ijkl[REDACTED]"},
		{"短密码", "password=abc", "password=[REDACTED]"},
		{"恰好四字", "token=abcd", "token=[REDACTED]"},
		{"Unicode字符", "secret=甲乙丙丁戊己", "secret=甲乙丙丁[REDACTED]"},
		{"引号空白", `password="abcd efgh" safe=yes`, `password="abcd[REDACTED]" safe=yes`},
		{"单引号", "password='abcd efgh' safe=yes", "password='abcd[REDACTED]' safe=yes"},
		{"JSON字段", `{"password":"abcdefgh","status":500}`, `{"password":"abcd[REDACTED]","status":500}`},
		{"Bearer", "Authorization: Bearer abcdefgh", "Authorization: Bearer abcd[REDACTED]"},
		{"Basic", "authorization=Basic YWJjZGVmZ2g=", "authorization=Basic YWJj[REDACTED]"},
		{"代理认证", "Proxy-Authorization: Bearer abcdefgh", "Proxy-Authorization: Bearer abcd[REDACTED]"},
		{"授权无边界", "Authorization: Bearer abcdefgh trailing diagnostics", "Authorization: Bearer abcd[REDACTED]"},
		{"引号授权", `{"Authorization":"Bearer abcdefgh","status":500}`, `{"Authorization":"Bearer abcd[REDACTED]","status":500}`},
		{"多行授权", "Authorization: Bearer abcdefgh\r\nstatus=500", `Authorization: Bearer abcd[REDACTED]\r\nstatus=500`},
		{"字面转义不切断凭据", `Authorization: Bearer abcd\ncredential-tail`, "Authorization: Bearer abcd[REDACTED]"},
		{"多Cookie", "Cookie: theme=dark; sid=abcdefgh; csrf=ijklmnop", "Cookie: theme=[REDACTED]; sid=abcd[REDACTED]; csrf=ijkl[REDACTED]"},
		{"引号Cookie", `Cookie: sid="abcd;efgh"; csrf=ijklmnop`, `Cookie: sid="abcd[REDACTED]"; csrf=ijkl[REDACTED]`},
		{"JSONCookie", `{"Cookie":"sid=abcdefgh; csrf=ijklmnop","status":500}`, `{"Cookie":"sid=abcd[REDACTED]; csrf=ijkl[REDACTED]","status":500}`},
		{"SetCookie", "Set-Cookie: sid=abcdefgh; Path=/; HttpOnly", "Set-Cookie: sid=abcd[REDACTED]; Path=[REDACTED]; Http[REDACTED]"},
		{"Cookie无边界", "Cookie: sid=abcdefgh diagnostic-tail", "Cookie: sid=abcd[REDACTED]"},
		{"不完整引号", `password="abcdefgh tail`, `password="abcd[REDACTED]`},
		{"Cookie不完整引号", `Cookie: sid="abcdefgh\`, `Cookie: sid="abc[REDACTED]`},
		{"空值", "password=", "password="},
		{"空引号值", `password=""`, `password=""`},
		{"空Cookie项", "Cookie: ; sid=abcdefgh;", "Cookie: ; sid=abcd[REDACTED];"},
		{"空授权值", "Authorization:", "Authorization:"},
		{"短授权", "Authorization: Bearer abcd", "Authorization: Bearer [REDACTED]"},
		{"短授权后有诊断", "Authorization: Bearer abcd trailing diagnostics", "Authorization: Bearer [REDACTED]"},
		{"短授权尾空白", "Authorization: Bearer abcd  ", "Authorization: Bearer [REDACTED]"},
		{"短Cookie后有诊断", "Cookie: sid=abcd trailing diagnostics", "Cookie: sid=[REDACTED]"},
		{"Cookie值前后空白", "Cookie: sid= abcd  ; csrf=ijklmnop", "Cookie: sid=[REDACTED]; csrf=ijkl[REDACTED]"},
		{"未知认证方案保守遮蔽", "Authorization: custom abcdefgh", "Authorization: cust[REDACTED]"},
		{"转义引号", `password="abcd\"efgh"`, `password="abcd[REDACTED]"`},
		{"凭据内出现键值", `password="abcd token=efghijkl"`, `password="abcd[REDACTED]"`},
		{"已遮蔽", "Authorization: Bearer abcd[REDACTED]", "Authorization: Bearer abcd[REDACTED]"},
		{"已全部遮蔽", "password=[REDACTED]", "password=[REDACTED]"},
		{"短别名pass", "pass=abcdefgh host=db", "pass=abcd[REDACTED] host=db"},
		{"短别名auth", "auth=abcdefgh mode=strict", "auth=abcd[REDACTED] mode=strict"},
		{"下划线短别名", "user_pass=abcdefgh login.auth=ijklmnop", "user_pass=abcd[REDACTED] login.auth=ijkl[REDACTED]"},
		{"驼峰与冒号短别名", "userPwd=abcdefgh basicAuth: ijklmnop db:pass=qrstuvwx", "userPwd=abcd[REDACTED] basicAuth: ijkl[REDACTED] db:pass=qrst[REDACTED]"},
		{"普通字段不误伤", "passport=A12345678 author=alice auth_status=ok pass_rate=0.99 compass=north bypass=false", "passport=A12345678 author=alice auth_status=ok pass_rate=0.99 compass=north bypass=false"},
		{"非敏感文本", "database unavailable host=db", "database unavailable host=db"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := SanitizeErrorText(testCase.input)
			if got != testCase.want {
				t.Fatalf("凭据脱敏结果错误: got=%q want=%q", got, testCase.want)
			}
			// 真换行转义后失去字段边界，再次脱敏允许保守移除后续诊断内容。
			if repeated := SanitizeErrorText(got); !strings.ContainsAny(testCase.input, "\r\n") && repeated != got {
				t.Fatalf("再次脱敏不应破坏已遮蔽文本: got=%q want=%q", repeated, got)
			}
		})
	}
}

func TestPublicErrorMasksCredentialDelimitersAndTheirSuffixes(t *testing.T) {
	for _, delimiter := range []string{",", ";", "&"} {
		for _, input := range []string{"password=" + delimiter + "private-tail", "token=begin" + delimiter + "private-tail"} {
			if output := SanitizeErrorTextFully(input + " code=500"); strings.Contains(output, "private-tail") ||
				!strings.Contains(output, "=[REDACTED] code=500") {
				t.Errorf("公开错误泄露含分隔符的凭据: %q", output)
			}
		}
	}
}

func TestJSONNumericCredentialBoundaryPreservesSafeFields(t *testing.T) {
	const expected = `{"token":"[REDACTED]","status":500}`
	for _, input := range []string{`{"token":1234,"status":500}`, `记录：{"token":1234,"status":500}`} {
		for _, sanitize := range []func(string) string{SanitizeErrorText, SanitizeErrorTextFully} {
			if output := sanitize(input); strings.Contains(output, "1234") || !strings.Contains(output, expected) {
				t.Errorf("JSON 的短数字凭据或后续字段处理不正确: %q", output)
			}
		}
	}
}

func TestCredentialPlaceholderInQuotedDigestCannotEndTheValue(t *testing.T) {
	input := `Authorization: Digest nonce="pref[REDACTED]\nCookie:private-tail=abcdefgh"`
	for _, sanitize := range []func(string) string{SanitizeErrorText, SanitizeErrorTextFully} {
		output := sanitize(input)
		if strings.Contains(output, "private-tail") || strings.Contains(output, "abcdefgh") {
			t.Errorf("真实凭据中的占位符或字面换行不能分割 HTTP 头: %q", output)
		}
	}
}

// FuzzErrorTextCredentialPrefixes 对任意字节生成合法长凭据，验证四字前缀和正文单行不变量。
func FuzzErrorTextCredentialPrefixes(f *testing.F) {
	for _, seed := range []string{"token", "汉字凭据", "quote\";cookie=tail", "\r\n"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		const maximumInputBytes = 512
		if len(raw) > maximumInputBytes {
			raw = raw[:maximumInputBytes]
		}
		credential := "abcd" + base64.RawURLEncoding.EncodeToString(raw) + "tail"
		for _, prefix := range []string{"Authorization: Bearer ", "password=", "Cookie: sid="} {
			sanitized := SanitizeErrorText(prefix + credential)
			if !strings.Contains(sanitized, "abcd[REDACTED]") || strings.Contains(sanitized, credential) {
				t.Fatalf("长凭据没有遵守前缀规则: %q", sanitized)
			}
		}
		if sanitized := SanitizeErrorText(string(raw)); strings.ContainsAny(sanitized, "\r\n\t") {
			t.Fatal("脱敏错误文本仍可插入日志行或制表符")
		}
	})
}

// TestFallbackCredentialPrefixes 确保前缀策略实际作用于失败驱动的兜底输出。
func TestFallbackCredentialPrefixes(t *testing.T) {
	logger := NewLog(&failingDriver{writeErr: errors.New("Authorization: Bearer abcd-secret-tail")})
	var fallback bytes.Buffer
	logger.SetFallbackWriter(&fallback)
	logger.Write("触发驱动错误", LevelError)
	if err := logger.Close(); err == nil {
		t.Fatal("驱动错误没有返回")
	}
	if output := fallback.String(); !strings.Contains(output, "Bearer abcd[REDACTED]") || strings.Contains(output, "secret-tail") {
		t.Fatalf("兜底日志未按前缀策略脱敏: %q", output)
	}
}
