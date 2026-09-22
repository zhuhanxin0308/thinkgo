package command

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
)

// TestControllerDiscoveryBatchRejectsStagingFailureWithoutChanges 验证全部临时文件
// 写完以前不会替换任何旧发现结果。
func TestControllerDiscoveryBatchRejectsStagingFailureWithoutChanges(t *testing.T) {
	application, generatedPaths := prepareControllerDiscoveryTransactionFixture(t, true)
	oldSources := readControllerDiscoverySnapshots(t, generatedPaths)
	addControllerDiscoveryTransactionTypes(t, application.BasePath)

	injected := errors.New("注入第二个临时文件写入失败")
	originalWriter := writeControllerDiscoveryStagedSource
	writes := 0
	writeControllerDiscoveryStagedSource = func(root *os.Root, relativePath string, source []byte, permission os.FileMode) error {
		writes++
		if writes == 2 {
			return injected
		}
		return originalWriter(root, relativePath, source, permission)
	}
	t.Cleanup(func() { writeControllerDiscoveryStagedSource = originalWriter })

	if err := RefreshControllerDiscovery(application); !errors.Is(err, injected) {
		t.Fatalf("临时文件写入失败应原样传播，实际为 %v", err)
	}
	assertControllerDiscoverySnapshots(t, generatedPaths, oldSources)
	assertNoControllerDiscoveryAuxiliaryFiles(t, application.BasePath)
}

// TestControllerDiscoveryBatchRollsBackPublishedFiles 验证后续文件发布失败时，
// 已经发布的新文件和已经移走的旧文件都会恢复到调用前版本。
func TestControllerDiscoveryBatchRollsBackPublishedFiles(t *testing.T) {
	application, generatedPaths := prepareControllerDiscoveryTransactionFixture(t, true)
	oldSources := readControllerDiscoverySnapshots(t, generatedPaths)
	addControllerDiscoveryTransactionTypes(t, application.BasePath)

	injected := errors.New("注入第二个目标替换失败")
	originalRename := renameControllerDiscoveryPath
	renames := 0
	renameControllerDiscoveryPath = func(root *os.Root, oldPath, newPath string) error {
		renames++
		if renames == 4 {
			return injected
		}
		return originalRename(root, oldPath, newPath)
	}
	t.Cleanup(func() { renameControllerDiscoveryPath = originalRename })

	if err := RefreshControllerDiscovery(application); !errors.Is(err, injected) {
		t.Fatalf("发布失败应原样传播，实际为 %v", err)
	}
	assertControllerDiscoverySnapshots(t, generatedPaths, oldSources)
	assertNoControllerDiscoveryAuxiliaryFiles(t, application.BasePath)
}

// TestControllerDiscoveryBatchRemovesNewFilesOnPublishFailure 验证首次生成中途失败时
// 不留下已经发布的前半批文件。
func TestControllerDiscoveryBatchRemovesNewFilesOnPublishFailure(t *testing.T) {
	application, generatedPaths := prepareControllerDiscoveryTransactionFixture(t, false)
	injected := errors.New("注入首次生成第二个目标发布失败")
	originalRename := renameControllerDiscoveryPath
	renames := 0
	renameControllerDiscoveryPath = func(root *os.Root, oldPath, newPath string) error {
		renames++
		if renames == 2 {
			return injected
		}
		return originalRename(root, oldPath, newPath)
	}
	t.Cleanup(func() { renameControllerDiscoveryPath = originalRename })

	if err := RefreshControllerDiscovery(application); !errors.Is(err, injected) {
		t.Fatalf("首次发布失败应原样传播，实际为 %v", err)
	}
	for _, generatedPath := range generatedPaths {
		if _, err := os.Stat(generatedPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("失败后不得留下生成文件 %q: %v", generatedPath, err)
		}
	}
	assertNoControllerDiscoveryAuxiliaryFiles(t, application.BasePath)
}

// TestControllerDiscoveryGenerationFailurePreservesOldBatch 验证解析或格式化阶段失败时
// 尚未进入发布阶段，旧批次保持逐字节不变。
func TestControllerDiscoveryGenerationFailurePreservesOldBatch(t *testing.T) {
	application, generatedPaths := prepareControllerDiscoveryTransactionFixture(t, true)
	oldSources := readControllerDiscoverySnapshots(t, generatedPaths)
	writeDiscoveryFixture(t, application.BasePath, "app/index/controller/index.go", "package controller\n\nfunc (")

	if err := RefreshControllerDiscovery(application); err == nil || !strings.Contains(err.Error(), "解析应用源码") {
		t.Fatalf("非法控制器源码应阻止整个批次，实际为 %v", err)
	}
	assertControllerDiscoverySnapshots(t, generatedPaths, oldSources)
	assertNoControllerDiscoveryAuxiliaryFiles(t, application.BasePath)
}

// TestControllerDiscoveryFormattingFailurePreservesOldBatch 验证任一生成源码
// 格式化失败时不会进入暂存和发布阶段，旧批次保持不变。
func TestControllerDiscoveryFormattingFailurePreservesOldBatch(t *testing.T) {
	application, generatedPaths := prepareControllerDiscoveryTransactionFixture(t, true)
	oldSources := readControllerDiscoverySnapshots(t, generatedPaths)
	addControllerDiscoveryTransactionTypes(t, application.BasePath)

	injected := errors.New("注入第二个生成文件格式化失败")
	originalFormatter := formatControllerDiscoverySource
	formatCalls := 0
	formatControllerDiscoverySource = func(source []byte) ([]byte, error) {
		formatCalls++
		if formatCalls == 2 {
			return nil, injected
		}
		return originalFormatter(source)
	}
	t.Cleanup(func() { formatControllerDiscoverySource = originalFormatter })

	if err := RefreshControllerDiscovery(application); !errors.Is(err, injected) {
		t.Fatalf("格式化失败应原样传播，实际为 %v", err)
	}
	assertControllerDiscoverySnapshots(t, generatedPaths, oldSources)
	assertNoControllerDiscoveryAuxiliaryFiles(t, application.BasePath)
}

// TestControllerDiscoveryConcurrentRefreshPublishesCompleteBatch 验证同一项目并发刷新
// 被串行化，最终只能观察到一套完整且可校验的发现结果。
func TestControllerDiscoveryConcurrentRefreshPublishesCompleteBatch(t *testing.T) {
	application, _ := prepareControllerDiscoveryTransactionFixture(t, false)
	const workers = 8
	var wait sync.WaitGroup
	errorsByWorker := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsByWorker <- RefreshControllerDiscovery(application)
		}()
	}
	wait.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatalf("并发刷新失败: %v", err)
		}
	}
	if err := CheckControllerDiscovery(application); err != nil {
		t.Fatalf("并发刷新后发现批次不完整: %v", err)
	}
	assertNoControllerDiscoveryAuxiliaryFiles(t, application.BasePath)
}

// TestControllerDiscoveryBatchPreservesConcurrentExternalChange 验证暂存期间若
// 目标被其他进程更新，发布会拒绝覆盖外部版本，并清理整批临时文件。
func TestControllerDiscoveryBatchPreservesConcurrentExternalChange(t *testing.T) {
	application, generatedPaths := prepareControllerDiscoveryTransactionFixture(t, true)
	oldSources := readControllerDiscoverySnapshots(t, generatedPaths)
	addControllerDiscoveryTransactionTypes(t, application.BasePath)
	externalSource := []byte("// 外部进程已更新\n")

	originalWriter := writeControllerDiscoveryStagedSource
	writes := 0
	writeControllerDiscoveryStagedSource = func(root *os.Root, relativePath string, source []byte, permission os.FileMode) error {
		if err := originalWriter(root, relativePath, source, permission); err != nil {
			return err
		}
		writes++
		if writes == 2 {
			return os.WriteFile(generatedPaths[0], externalSource, 0o644)
		}
		return nil
	}
	t.Cleanup(func() { writeControllerDiscoveryStagedSource = originalWriter })

	err := RefreshControllerDiscovery(application)
	if err == nil || !strings.Contains(err.Error(), "在批次发布期间已变化") {
		t.Fatalf("并发外部更新必须阻止发布，实际为 %v", err)
	}
	current, readErr := os.ReadFile(generatedPaths[0])
	if readErr != nil || string(current) != string(externalSource) {
		t.Fatalf("外部更新不得被生成器覆盖: content=%q err=%v", current, readErr)
	}
	assertControllerDiscoverySnapshots(t, generatedPaths[1:], oldSources)
	assertNoControllerDiscoveryAuxiliaryFiles(t, application.BasePath)
}

func prepareControllerDiscoveryTransactionFixture(t *testing.T, generate bool) (*framework.App, []string) {
	t.Helper()
	basePath := t.TempDir()
	writeDiscoveryModuleFixture(t, basePath, "example.com/transaction")
	writeDiscoveryFixture(t, basePath, "app/admin/controller/user.go", "package controller\n\ntype User struct{}\n\nfunc (*User) Index() string { return \"admin\" }\n")
	writeDiscoveryFixture(t, basePath, "app/index/controller/index.go", "package controller\n\ntype Index struct{}\n\nfunc (*Index) Index() string { return \"index\" }\n")
	application := &framework.App{BasePath: basePath, ApplicationPath: filepath.Join(basePath, "app")}
	generatedPaths := []string{
		filepath.Join(basePath, "app", "admin", applicationDiscoveryFilename),
		filepath.Join(basePath, "app", "index", applicationDiscoveryFilename),
		filepath.Join(basePath, "app", applicationDiscoveryFilename),
	}
	if generate {
		if err := RefreshControllerDiscovery(application); err != nil {
			t.Fatalf("准备旧发现批次失败: %v", err)
		}
	}
	return application, generatedPaths
}

func addControllerDiscoveryTransactionTypes(t *testing.T, basePath string) {
	t.Helper()
	writeDiscoveryFixture(t, basePath, "app/admin/controller/user.go", "package controller\n\ntype User struct{}\nfunc (*User) Index() string { return \"admin\" }\n\ntype Audit struct{}\nfunc (*Audit) Index() string { return \"audit\" }\n")
	writeDiscoveryFixture(t, basePath, "app/index/controller/index.go", "package controller\n\ntype Index struct{}\nfunc (*Index) Index() string { return \"index\" }\n\ntype Health struct{}\nfunc (*Health) Index() string { return \"health\" }\n")
}

func readControllerDiscoverySnapshots(t *testing.T, paths []string) map[string][]byte {
	t.Helper()
	snapshots := make(map[string][]byte, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取旧发现文件 %q 失败: %v", path, err)
		}
		snapshots[path] = content
	}
	return snapshots
}

func assertControllerDiscoverySnapshots(t *testing.T, paths []string, expected map[string][]byte) {
	t.Helper()
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取回滚后的发现文件 %q 失败: %v", path, err)
		}
		if string(content) != string(expected[path]) {
			t.Fatalf("发现文件 %q 未恢复到旧版本", path)
		}
	}
}

func assertNoControllerDiscoveryAuxiliaryFiles(t *testing.T, basePath string) {
	t.Helper()
	err := filepath.WalkDir(basePath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if strings.Contains(name, ".discovery-tmp-") || strings.Contains(name, ".discovery-backup-") {
			t.Errorf("发现事务残留辅助文件 %q", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描发现事务残留失败: %v", err)
	}
}
