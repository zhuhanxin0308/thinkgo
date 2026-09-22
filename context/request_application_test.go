package context

import "testing"

// TestBuildApplicationPathSupportsRelativeBusinessPaths 验证业务代码可直接传入
// 相对资源路径，同时拒绝把绝对 URL 或协议相对 URL伪装成站内地址。
func TestBuildApplicationPathSupportsRelativeBusinessPaths(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		expected  string
		wantError bool
	}{
		{name: "relative", path: "assets/app.css?version=1", expected: "/admin/assets/app.css?version=1"},
		{name: "already prefixed", path: "/admin/dashboard", expected: "/admin/dashboard"},
		{name: "absolute URL", path: "https://evil.example/app.css", wantError: true},
		{name: "protocol relative URL", path: "//evil.example/app.css", wantError: true},
		{name: "control character", path: "/asset\nname", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := BuildApplicationPath("/admin", test.path)
			if test.wantError {
				if err == nil {
					t.Fatalf("非法路径必须失败: %q", actual)
				}
				return
			}
			if err != nil || actual != test.expected {
				t.Fatalf("应用路径错误: got=%q want=%q err=%v", actual, test.expected, err)
			}
		})
	}
}
