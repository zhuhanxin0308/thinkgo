package winfile

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestSharedGuardRetainsEachOwnerUntilItsClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.guard")
	first, err := TrySharedGuard(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := TrySharedGuard(path)
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	probe := func() error {
		return TryWithGuard(context.Background(), path, func() error { return nil })
	}
	if err := probe(); !errors.Is(err, ErrGuardInUse) {
		t.Errorf("独占探测未发现共享持有者: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := probe(); !errors.Is(err, ErrGuardInUse) {
		t.Errorf("仍有共享持有者时锁被提前释放: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := WithGuard(context.Background(), path, func() error {
		held, err := TrySharedGuard(path)
		if held != nil || !errors.Is(err, ErrGuardInUse) {
			t.Errorf("共享探测不能覆盖独占持有者: %v %v", held, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
