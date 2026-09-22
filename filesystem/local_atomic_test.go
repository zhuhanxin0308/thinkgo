package filesystem

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestLocalAtomicWritePreservesPublishedTargets 验证写入流失败时，旧文件不会被截断，
// 新文件也不会以半成品形式暴露。
func TestLocalAtomicWritePreservesPublishedTargets(t *testing.T) {
	disk, rootPath := newAtomicTestDisk(t)
	if err := disk.Write("existing.txt", "old-content"); err != nil {
		t.Fatalf("准备旧目标失败: %v", err)
	}

	writeFailure := errors.New("write stream failed")
	if err := disk.WriteStream("existing.txt", &failAfterDataReader{data: []byte("partial-new"), err: writeFailure}); !errors.Is(err, writeFailure) {
		t.Fatalf("旧目标失败写入必须返回原始错误: %v", err)
	}
	assertLocalFileContent(t, disk, "existing.txt", "old-content")

	if err := disk.WriteStream("new.txt", &failAfterDataReader{data: []byte("partial-new"), err: writeFailure}); !errors.Is(err, writeFailure) {
		t.Fatalf("新目标失败写入必须返回原始错误: %v", err)
	}
	if exists, err := disk.FileExists("new.txt"); err != nil || exists {
		t.Fatalf("失败的新目标不得留下半文件: exists=%t err=%v", exists, err)
	}
	assertNoLocalTemporaryFiles(t, rootPath)
}

// TestLocalAtomicCopyAndUploadFailuresPreserveTargets 验证 Copy 和上传复用同一原子发布路径，
// 任意复制错误都不会破坏已发布目标。
func TestLocalAtomicCopyAndUploadFailuresPreserveTargets(t *testing.T) {
	t.Run("copy", func(t *testing.T) {
		disk, rootPath := newAtomicTestDisk(t)
		if err := disk.Write("source.txt", "complete-source"); err != nil {
			t.Fatalf("准备复制源失败: %v", err)
		}
		if err := disk.Write("destination.txt", "old-destination"); err != nil {
			t.Fatalf("准备复制目标失败: %v", err)
		}
		copyFailure := errors.New("copy failed")
		disk.operations.copy = partialCopyFailure(copyFailure)

		if err := disk.Copy("source.txt", "destination.txt"); !errors.Is(err, copyFailure) {
			t.Fatalf("Copy 必须返回复制错误: %v", err)
		}
		assertLocalFileContent(t, disk, "destination.txt", "old-destination")
		if err := disk.Copy("source.txt", "new-destination.txt"); !errors.Is(err, copyFailure) {
			t.Fatalf("新 Copy 目标必须返回复制错误: %v", err)
		}
		if exists, err := disk.FileExists("new-destination.txt"); err != nil || exists {
			t.Fatalf("失败的 Copy 不得留下新目标: exists=%t err=%v", exists, err)
		}
		assertNoLocalTemporaryFiles(t, rootPath)
	})

	t.Run("upload", func(t *testing.T) {
		disk, rootPath := newAtomicTestDisk(t)
		if err := disk.Write("uploads/avatar.txt", "old-avatar"); err != nil {
			t.Fatalf("准备上传目标失败: %v", err)
		}
		upload := newUploadFileHeader(t, "avatar.txt", []byte("complete-avatar"))
		uploadFailure := errors.New("upload copy failed")
		disk.operations.copy = partialCopyFailure(uploadFailure)

		if stored, err := disk.PutFileAs("uploads", upload, "avatar.txt"); !errors.Is(err, uploadFailure) || stored != "" {
			t.Fatalf("上传复制失败结果错误: stored=%q err=%v", stored, err)
		}
		assertLocalFileContent(t, disk, "uploads/avatar.txt", "old-avatar")
		if stored, err := disk.PutFileAs("uploads", upload, "new-avatar.txt"); !errors.Is(err, uploadFailure) || stored != "" {
			t.Fatalf("新上传目标失败结果错误: stored=%q err=%v", stored, err)
		}
		if exists, err := disk.FileExists("uploads/new-avatar.txt"); err != nil || exists {
			t.Fatalf("失败上传不得留下新目标: exists=%t err=%v", exists, err)
		}
		assertNoLocalTemporaryFiles(t, rootPath)
	})
}

// TestLocalAtomicPreCommitFailuresPreserveTarget 验证权限、文件同步、临时文件关闭和
// 原子替换失败都发生在发布前，旧目标始终保持不变。
func TestLocalAtomicPreCommitFailuresPreserveTarget(t *testing.T) {
	testCases := []struct {
		name   string
		inject func(*Local, error)
	}{
		{
			name: "chmod",
			inject: func(disk *Local, failure error) {
				disk.operations.chmod = func(*os.Root, string, os.FileMode) error { return failure }
			},
		},
		{
			name: "sync",
			inject: func(disk *Local, failure error) {
				disk.operations.syncFile = func(*os.File) error { return failure }
			},
		},
		{
			name: "close",
			inject: func(disk *Local, failure error) {
				disk.operations.closeTemporary = func(file *os.File) error {
					return errors.Join(file.Close(), failure)
				}
			},
		},
		{
			name: "replace",
			inject: func(disk *Local, failure error) {
				disk.operations.replace = func(*os.Root, string, string) error { return failure }
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			disk, rootPath := newAtomicTestDisk(t)
			if err := disk.Write("target.txt", "old-content"); err != nil {
				t.Fatalf("准备旧目标失败: %v", err)
			}
			failure := errors.New(testCase.name + " failed")
			testCase.inject(disk, failure)
			if err := disk.Write("target.txt", "new-content", map[string]interface{}{"visibility": VisibilityPublic}); !errors.Is(err, failure) {
				t.Fatalf("必须传播 %s 错误: %v", testCase.name, err)
			}
			assertLocalFileContent(t, disk, "target.txt", "old-content")
			assertNoLocalTemporaryFiles(t, rootPath)
		})
	}
}

// TestLocalAtomicWritePreservesExistingMode 验证未显式指定可见性时，原子替换不会
// 因新建临时文件而改变既有目标的权限位。
func TestLocalAtomicWritePreservesExistingMode(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "storage")
	disk, err := NewLocal(LocalConfig{Root: rootPath})
	if err != nil {
		t.Fatalf("创建权限兼容测试磁盘失败: %v", err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	if err = disk.Write("target.txt", "old"); err != nil {
		t.Fatalf("准备权限兼容目标失败: %v", err)
	}
	if err = disk.root.Chmod("target.txt", 0o640); err != nil {
		t.Fatalf("设置既有目标权限失败: %v", err)
	}
	before, err := disk.root.Stat("target.txt")
	if err != nil {
		t.Fatalf("读取写入前权限失败: %v", err)
	}
	if err = disk.Write("target.txt", "new"); err != nil {
		t.Fatalf("原子替换既有目标失败: %v", err)
	}
	after, err := disk.root.Stat("target.txt")
	if err != nil {
		t.Fatalf("读取写入后权限失败: %v", err)
	}
	if before.Mode().Perm() != after.Mode().Perm() {
		t.Fatalf("既有目标权限被改变: before=%#o after=%#o", before.Mode().Perm(), after.Mode().Perm())
	}
}

// TestLocalAtomicSourceCloseFailuresDoNotPublish 验证 Copy 和 Upload 的源流只有在
// 成功关闭后才允许发布临时文件。
func TestLocalAtomicSourceCloseFailuresDoNotPublish(t *testing.T) {
	t.Run("copy", func(t *testing.T) {
		disk, rootPath := newAtomicTestDisk(t)
		if err := disk.Write("source.txt", "source"); err != nil {
			t.Fatalf("准备复制源失败: %v", err)
		}
		if err := disk.Write("target.txt", "old"); err != nil {
			t.Fatalf("准备复制目标失败: %v", err)
		}
		closeFailure := errors.New("source close failed")
		disk.operations.closeSource = func(source io.Closer) error {
			return errors.Join(source.Close(), closeFailure)
		}
		if err := disk.Copy("source.txt", "target.txt"); !errors.Is(err, closeFailure) {
			t.Fatalf("Copy 必须传播源关闭错误: %v", err)
		}
		assertLocalFileContent(t, disk, "target.txt", "old")
		assertNoLocalTemporaryFiles(t, rootPath)
	})

	t.Run("upload", func(t *testing.T) {
		disk, rootPath := newAtomicTestDisk(t)
		if err := disk.Write("uploads/avatar.txt", "old"); err != nil {
			t.Fatalf("准备上传目标失败: %v", err)
		}
		closeFailure := errors.New("upload close failed")
		disk.operations.closeSource = func(source io.Closer) error {
			return errors.Join(source.Close(), closeFailure)
		}
		upload := newUploadFileHeader(t, "avatar.txt", []byte("new"))
		if stored, err := disk.PutFileAs("uploads", upload, "avatar.txt"); !errors.Is(err, closeFailure) || stored != "" {
			t.Fatalf("Upload 必须传播源关闭错误: stored=%q err=%v", stored, err)
		}
		assertLocalFileContent(t, disk, "uploads/avatar.txt", "old")
		assertNoLocalTemporaryFiles(t, rootPath)
	})
}

// TestLocalAtomicReadersOnlyObserveCompleteVersions 验证并发读取者在慢速写入期间只能
// 读取旧版本或完整新版本，绝不能看到临时内容。
func TestLocalAtomicReadersOnlyObserveCompleteVersions(t *testing.T) {
	disk, _ := newAtomicTestDisk(t)
	oldContent := strings.Repeat("old-version-", 8192)
	newContent := strings.Repeat("new-version-", 8192)
	if err := disk.Write("shared.txt", oldContent); err != nil {
		t.Fatalf("准备并发旧版本失败: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- disk.WriteStream("shared.txt", &gatedReader{
			data:    []byte(newContent),
			started: started,
			release: release,
		})
	}()
	<-started

	stopReaders := make(chan struct{})
	readerFailure := make(chan error, 1)
	var readers sync.WaitGroup
	for index := 0; index < 8; index++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stopReaders:
					return
				default:
				}
				content, err := disk.Read("shared.txt")
				if err != nil {
					select {
					case readerFailure <- err:
					default:
					}
					return
				}
				if content != oldContent && content != newContent {
					select {
					case readerFailure <- errors.New("读取到未完整发布的文件版本"):
					default:
					}
					return
				}
			}
		}()
	}
	close(release)
	if err := <-writerDone; err != nil {
		t.Fatalf("并发原子写入失败: %v", err)
	}
	close(stopReaders)
	readers.Wait()
	select {
	case err := <-readerFailure:
		t.Fatal(err)
	default:
	}
	assertLocalFileContent(t, disk, "shared.txt", newContent)
}

// TestLocalAtomicCommittedErrorsAreObservable 验证原子替换或 Move 已经完成后发生的
// 同步、权限错误会明确携带 ErrFilesystemOperationCommitted。
func TestLocalAtomicCommittedErrorsAreObservable(t *testing.T) {
	t.Run("write directory sync", func(t *testing.T) {
		disk, _ := newAtomicTestDisk(t)
		syncFailure := errors.New("directory sync failed")
		disk.operations.syncDirectory = func(*os.Root, string) error { return syncFailure }
		err := disk.Write("published.txt", "published")
		if !errors.Is(err, ErrFilesystemOperationCommitted) || !errors.Is(err, syncFailure) {
			t.Fatalf("已发布写入必须返回部分提交标记和同步错误: %v", err)
		}
		assertLocalFileContent(t, disk, "published.txt", "published")
	})

	t.Run("move chmod", func(t *testing.T) {
		disk, _ := newAtomicTestDisk(t)
		if err := disk.Write("source.txt", "moved"); err != nil {
			t.Fatalf("准备移动源失败: %v", err)
		}
		chmodFailure := errors.New("move chmod failed")
		baseChmod := disk.operations.chmod
		disk.operations.chmod = func(root *os.Root, name string, mode os.FileMode) error {
			if name == "destination.txt" {
				return chmodFailure
			}
			return baseChmod(root, name, mode)
		}
		err := disk.Move("source.txt", "destination.txt", map[string]interface{}{"visibility": VisibilityPublic})
		if !errors.Is(err, ErrFilesystemOperationCommitted) || !errors.Is(err, chmodFailure) {
			t.Fatalf("Move chmod 失败必须标记路径已移动: %v", err)
		}
		if exists, checkErr := disk.FileExists("source.txt"); checkErr != nil || exists {
			t.Fatalf("已提交 Move 的源路径必须消失: exists=%t err=%v", exists, checkErr)
		}
		assertLocalFileContent(t, disk, "destination.txt", "moved")
	})
}

type failAfterDataReader struct {
	data []byte
	err  error
	done bool
}

func (reader *failAfterDataReader) Read(target []byte) (int, error) {
	if reader.done {
		return 0, reader.err
	}
	reader.done = true
	return copy(target, reader.data), nil
}

type gatedReader struct {
	data    []byte
	started chan struct{}
	release chan struct{}
	offset  int
}

func (reader *gatedReader) Read(target []byte) (int, error) {
	if reader.offset == 0 {
		chunkSize := len(reader.data) / 2
		if chunkSize > len(target) {
			chunkSize = len(target)
		}
		written := copy(target, reader.data[:chunkSize])
		reader.offset = written
		close(reader.started)
		return written, nil
	}
	if reader.release != nil {
		<-reader.release
		reader.release = nil
	}
	if reader.offset >= len(reader.data) {
		return 0, io.EOF
	}
	written := copy(target, reader.data[reader.offset:])
	reader.offset += written
	return written, nil
}

func partialCopyFailure(failure error) func(io.Writer, io.Reader) (int64, error) {
	return func(destination io.Writer, _ io.Reader) (int64, error) {
		written, err := destination.Write([]byte("partial-copy"))
		return int64(written), errors.Join(err, failure)
	}
}

func newAtomicTestDisk(t *testing.T) (*Local, string) {
	t.Helper()
	rootPath := filepath.Join(t.TempDir(), "storage")
	disk, err := NewLocal(LocalConfig{Root: rootPath, Visibility: VisibilityPrivate})
	if err != nil {
		t.Fatalf("创建原子写入测试磁盘失败: %v", err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	return disk, rootPath
}

func assertLocalFileContent(t *testing.T, disk *Local, name string, expected string) {
	t.Helper()
	actual, err := disk.Read(name)
	if err != nil || actual != expected {
		t.Fatalf("文件内容错误: name=%q actual=%q expected=%q err=%v", name, actual, expected, err)
	}
}

func assertNoLocalTemporaryFiles(t *testing.T, rootPath string) {
	t.Helper()
	err := filepath.WalkDir(rootPath, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if strings.HasPrefix(entry.Name(), ".thinkgo-tmp-") {
			return errors.New("原子写入遗留临时文件: " + current)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
