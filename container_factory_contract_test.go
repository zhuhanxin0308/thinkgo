package framework

import "testing"

// TestInvalidFactorySignatureDoesNotExecute 验证各生命周期都在工厂产生副作用前拒绝非法返回签名。
func TestInvalidFactorySignatureDoesNotExecute(t *testing.T) {
	for _, lifecycle := range []LifecycleType{Singleton, Factory, Scoped} {
		container := NewContainer()
		scope := container.NewScope()
		t.Cleanup(func() { _ = scope.Close() })
		calls := 0
		for _, factory := range []interface{}{
			func() { calls++ },
			func() (int, string, bool) { calls++; return 1, "", false },
			func() (int, string) { calls++; return 1, "" },
		} {
			switch lifecycle {
			case Singleton:
				container.Bind("invalid", factory)
			case Factory:
				container.BindFactory("invalid", factory)
			case Scoped:
				container.BindScoped("invalid", factory)
			}
			if _, err := scope.Make("invalid"); err == nil {
				t.Errorf("生命周期 %v 未拒绝非法工厂 %T", lifecycle, factory)
			}
		}
		if calls != 0 {
			t.Errorf("生命周期 %v 执行了 %d 次非法工厂", lifecycle, calls)
		}
	}
}
