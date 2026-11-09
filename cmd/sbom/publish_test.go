package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
)

type publicationCandidate struct {
	ID      int
	ready   chan<- struct{}
	release <-chan struct{}
}

func (candidate publicationCandidate) MarshalJSON() ([]byte, error) {
	candidate.ready <- struct{}{}
	<-candidate.release
	return json.Marshal(map[string]int{"id": candidate.ID})
}

// TestWriteJSONConcurrentPublication 验证真实公开写入路径只发布一份完整候选。
func TestWriteJSONConcurrentPublication(t *testing.T) {
	const writers = 16
	output := filepath.Join(t.TempDir(), "bom.json")
	ready, release := make(chan struct{}, writers), make(chan struct{})
	results := make([]error, writers)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[index] = writeJSON(output, publicationCandidate{ID: index, ready: ready, release: release})
		}()
	}
	for range writers {
		<-ready
	}
	close(release)
	wait.Wait()
	winner := -1
	for index, err := range results {
		if err == nil {
			if winner >= 0 {
				t.Fatalf("多个发布者成功，先前产物可能已被覆盖: %d, %d", winner, index)
			}
			winner = index
		} else if !errors.Is(err, os.ErrExist) {
			t.Errorf("发布冲突未保留文件存在错误: %v", err)
		}
	}
	if winner < 0 {
		t.Fatal("没有候选成功发布")
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]int
	if err := json.Unmarshal(content, &result); err != nil || result["id"] != winner {
		t.Fatalf("最终内容并非成功方的完整 JSON: %s %v", content, err)
	}
	assertNoSBOMTemporaryFiles(t, output)
}

// TestWriteJSONPreservesDanglingSymlink 验证目标名称已占用时不能覆盖，即使链接目标尚不存在。
func TestWriteJSONPreservesDanglingSymlink(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "bom.json")
	missing := filepath.Join(root, "missing")
	if err := os.Symlink(missing, output); err != nil {
		// Windows SDK 的 ERROR_PRIVILEGE_NOT_HELD：未开启开发者模式时缺少符号链接权限。
		const windowsPrivilegeNotHeld = syscall.Errno(1314)
		if errors.Is(err, os.ErrPermission) || runtime.GOOS == "windows" && errors.Is(err, windowsPrivilegeNotHeld) {
			t.Skip("当前环境不允许创建符号链接")
		}
		t.Fatal(err)
	}
	if err := writeJSON(output, map[string]int{"id": 1}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("已占用输出未拒绝: %v", err)
	}
	if target, err := os.Readlink(output); err != nil || target != missing {
		t.Fatalf("原有链接已被替换: %q %v", target, err)
	}
	assertNoSBOMTemporaryFiles(t, output)
}

// TestCompletedSBOMCandidatesPublishExclusively 在两份候选均写完后同时发布，恰好一方成功。
func TestCompletedSBOMCandidatesPublishExclusively(t *testing.T) {
	output := filepath.Join(t.TempDir(), "bom.json")
	ready, release := make(chan struct{}, 2), make(chan struct{})
	results := make(chan error, 2)
	for index := range 2 {
		go func() {
			results <- writeJSONWithPublisher(output, map[string]int{"id": index}, func(source, target string) error {
				content, err := os.ReadFile(source)
				if err != nil || !json.Valid(content) {
					t.Errorf("同步点之前候选尚未写完: %v", err)
				}
				ready <- struct{}{}
				<-release
				return os.Link(source, target)
			})
		}()
	}
	<-ready
	<-ready
	close(release)
	first, second := <-results, <-results
	if first != nil && second != nil || first == nil && second == nil {
		t.Fatalf("排他发布必须恰好一方成功: %v %v", first, second)
	}
	if first != nil && !errors.Is(first, os.ErrExist) || second != nil && !errors.Is(second, os.ErrExist) {
		t.Fatalf("失败方必须报告输出存在: %v %v", first, second)
	}
	assertNoSBOMTemporaryFiles(t, output)
}

func assertNoSBOMTemporaryFiles(t *testing.T, output string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(filepath.Dir(output), ".sbom-*.tmp"))
	if err != nil || len(files) != 0 {
		t.Fatalf("临时发布文件未清理: %v %v", files, err)
	}
}

// TestWriteJSONPublicationInterleavings 在候选已完整写入的同步点验证抢先发布与文件系统拒绝。
func TestWriteJSONPublicationInterleavings(t *testing.T) {
	for _, external := range []bool{false, true} {
		output := filepath.Join(t.TempDir(), "bom.json")
		err := writeJSONWithPublisher(output, map[string]string{"candidate": "complete"},
			func(temporary, target string) error {
				content, err := os.ReadFile(temporary)
				if err != nil || !json.Valid(content) {
					t.Fatalf("发布前候选文件不完整: %s %v", content, err)
				}
				if !external {
					return os.ErrPermission
				}
				if err := os.WriteFile(target, []byte("external-winner"), 0o600); err != nil {
					t.Fatal(err)
				}
				return os.Link(temporary, target)
			})
		if external {
			if !errors.Is(err, os.ErrExist) {
				t.Fatalf("抢先发布未报告冲突: %v", err)
			}
			if content, err := os.ReadFile(output); err != nil || string(content) != "external-winner" {
				t.Fatalf("抢先发布内容被覆盖: %s %v", content, err)
			}
		} else {
			if !errors.Is(err, os.ErrPermission) {
				t.Fatalf("不支持发布时应保留错误: %v", err)
			}
			if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("发布失败却遗留输出: %v", err)
			}
		}
		assertNoSBOMTemporaryFiles(t, output)
	}
}

func TestWriteJSONEncodingFailureDoesNotPublish(t *testing.T) {
	output := filepath.Join(t.TempDir(), "bom.json")
	if err := writeJSON(output, make(chan int)); err == nil {
		t.Fatal("不可编码内容应失败")
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("编码失败不应发布内容: %v", err)
	}
	assertNoSBOMTemporaryFiles(t, output)
}
