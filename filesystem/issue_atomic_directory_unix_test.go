//go:build unix

package filesystem

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestIssue135RejectsSharedAtomicParentsBeforeReading(t *testing.T) {
	for _, test := range []struct {
		name   string
		unsafe string
		target string
		mode   os.FileMode
	}{
		{name: "shared root", target: "new/output.txt", mode: 0o777},
		{name: "shared final parent", unsafe: "shared", target: "shared/output.txt", mode: 0o777},
		{name: "group writable parent", unsafe: "shared", target: "shared/output.txt", mode: 0o775},
		{name: "shared ancestor", unsafe: "shared", target: "shared/safe/new/output.txt", mode: 0o777},
		{name: "sticky shared parent", unsafe: "shared", target: "shared/output.txt", mode: os.ModeSticky | 0o777},
	} {
		t.Run(test.name, func(t *testing.T) {
			disk, root := newAtomicTestDisk(t)
			if err := os.MkdirAll(filepath.Join(root, "shared", "safe"), 0o700); err != nil {
				t.Fatal(err)
			}
			unsafe := filepath.Join(root, test.unsafe)
			if err := os.Chmod(unsafe, test.mode); err != nil {
				t.Fatal(err)
			}
			read := false
			disk.operations.copy = func(io.Writer, io.Reader) (int64, error) {
				read = true
				return 0, errors.New("content must not be consumed")
			}
			if err := disk.Write(test.target, "not consumed", map[string]interface{}{"visibility": VisibilityPublic}); !errors.Is(err, ErrUnsafeLocalWriteDirectory) {
				t.Fatalf("shared directory must be rejected: %v", err)
			}
			if read {
				t.Fatal("untrusted-directory rejection consumed content")
			}
			for _, missing := range []string{test.target, "new", "shared/safe/new"} {
				if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(missing))); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("rejection created %q: %v", missing, err)
				}
			}
			assertNoLocalTemporaryFiles(t, root)
		})
	}
}

func TestIssue135RejectsSymlinkParentsAndUnsafeCreationMode(t *testing.T) {
	t.Run("root-contained symlink parent", func(t *testing.T) {
		disk, root := newAtomicTestDisk(t)
		if err := os.Mkdir(filepath.Join(root, "actual"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("actual", filepath.Join(root, "linked")); err != nil {
			t.Fatal(err)
		}
		err := disk.Write("linked/new/output.txt", "rejected")
		if !errors.Is(err, ErrUnsafeLocalWriteDirectory) || !errors.Is(err, ErrSymbolicLink) {
			t.Fatalf("symlink parent must be rejected: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(root, "actual", "new")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("symlink rejection created a directory: %v", err)
		}
		assertNoLocalTemporaryFiles(t, root)
	})
	t.Run("unsafe configured creation mode", func(t *testing.T) {
		root := t.TempDir()
		disk, err := NewLocal(LocalConfig{Root: root, Visibility: VisibilityPublic, Permissions: Permissions{DirectoryPublic: 0o777}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = disk.Close() })
		if err := disk.Write("new/child/output.txt", "rejected"); !errors.Is(err, ErrUnsafeLocalWriteDirectory) {
			t.Fatalf("unsafe creation mode must be rejected: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(root, "new")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsafe creation mode created a directory: %v", err)
		}
	})
}

func TestIssue135CopyAndUploadRejectSharedDestinations(t *testing.T) {
	disk, root := newAtomicTestDisk(t)
	if err := disk.Write("source.txt", "source"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "shared"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "shared"), 0o777); err != nil {
		t.Fatal(err)
	}
	disk.operations.copy = func(io.Writer, io.Reader) (int64, error) {
		t.Error("shared destination reached the content copy")
		return 0, errors.New("unexpected copy")
	}
	if err := disk.Copy("source.txt", "shared/copy.txt"); !errors.Is(err, ErrUnsafeLocalWriteDirectory) {
		t.Fatalf("Copy must enforce the atomic directory policy: %v", err)
	}
	upload := newUploadFileHeader(t, "upload.txt", []byte("dummy"))
	stored, err := disk.PutFileAs("shared", upload, "upload.txt", map[string]interface{}{"trusted_upload": true})
	if !errors.Is(err, ErrUnsafeLocalWriteDirectory) || stored != "" {
		t.Fatalf("upload must enforce the atomic directory policy: stored=%q err=%v", stored, err)
	}
	assertLocalFileContent(t, disk, "source.txt", "source")
	assertNoLocalTemporaryFiles(t, root)
}

// These same-UID hooks exercise defensive identity checks. They are not the
// race-safety argument: separate unprivileged UIDs are excluded by the parent
// ownership/mode checks before an atomic temporary can be created.
func TestIssue135TemporaryReplacementIsNotChmoddedPublishedOrRemoved(t *testing.T) {
	for _, phase := range []string{"copy", "chmod", "close", "hardlink"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			disk, err := NewLocal(LocalConfig{Root: root, Visibility: VisibilityPublic})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = disk.Close() })
			if err := disk.Write("shared/output.txt", "old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "private"), 0o755); err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(root, "private", "fixture.txt")
			if err := os.WriteFile(victim, []byte("private dummy"), 0o600); err != nil {
				t.Fatal(err)
			}
			replacement := ""
			replaceTemporary := func() error {
				if replacement != "" {
					return nil
				}
				entries, err := os.ReadDir(filepath.Join(root, "shared"))
				if err != nil {
					return err
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), localTemporaryPrefix) {
						replacement = filepath.Join(root, "shared", entry.Name())
						if err := os.Remove(replacement); err != nil {
							return err
						}
						if phase == "hardlink" {
							return os.Link(victim, replacement)
						}
						return os.Symlink("../private/fixture.txt", replacement)
					}
				}
				return errors.New("temporary fixture not found")
			}
			base := disk.operations
			switch phase {
			case "copy", "hardlink":
				disk.operations.copy = func(writer io.Writer, reader io.Reader) (int64, error) {
					written, err := base.copy(writer, reader)
					return written, errors.Join(err, replaceTemporary())
				}
			case "chmod":
				disk.operations.chmodTemporary = func(file *os.File, mode os.FileMode) error {
					if err := replaceTemporary(); err != nil {
						return err
					}
					return base.chmodTemporary(file, mode)
				}
			case "close":
				disk.operations.closeTemporary = func(file *os.File) error {
					return errors.Join(base.closeTemporary(file), replaceTemporary())
				}
			}
			if err := disk.Write("shared/output.txt", "new public content"); !errors.Is(err, errLocalTemporaryChanged) {
				t.Fatalf("replacement must abort publication: %v", err)
			}
			information, err := os.Stat(victim)
			if err != nil || information.Mode().Perm() != 0o600 {
				t.Fatalf("private file permissions changed: info=%v err=%v", information, err)
			}
			assertLocalFileContent(t, disk, "private/fixture.txt", "private dummy")
			assertLocalFileContent(t, disk, "shared/output.txt", "old")
			replacementInfo, err := os.Lstat(replacement)
			if err != nil {
				t.Fatalf("failure path removed or published the replacement: %v", err)
			}
			if phase == "hardlink" {
				if !replacementInfo.Mode().IsRegular() || !os.SameFile(information, replacementInfo) {
					t.Fatal("failure path modified the replacement hardlink")
				}
			} else if replacementInfo.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("failure path modified the replacement symlink: info=%v", replacementInfo)
			}
		})
	}
}

type localDirectoryOwnerFixture struct {
	os.FileInfo
	stat *syscall.Stat_t
}

func (fixture localDirectoryOwnerFixture) Sys() interface{} { return fixture.stat }

func TestIssue135DirectoryOwnerMustRemainTrusted(t *testing.T) {
	information, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stat := *information.Sys().(*syscall.Stat_t)
	for _, owner := range []uint32{stat.Uid, 0} {
		trusted := stat
		trusted.Uid = owner
		if err := validateLocalDirectoryOwnership(localDirectoryOwnerFixture{FileInfo: information, stat: &trusted}); err != nil {
			t.Fatalf("trusted owner %d rejected: %v", owner, err)
		}
	}
	foreign := stat
	foreign.Uid = 12345
	if foreign.Uid == stat.Uid {
		foreign.Uid++
	}
	if err := validateLocalDirectoryOwnership(localDirectoryOwnerFixture{FileInfo: information, stat: &foreign}); !errors.Is(err, ErrUnsafeLocalWriteDirectory) {
		t.Fatalf("foreign owner must be rejected even with private mode: %v", err)
	}
	if err := validateLocalDirectoryOwnership(localDirectoryOwnerFixture{FileInfo: information}); !errors.Is(err, ErrUnsafeLocalWriteDirectory) {
		t.Fatalf("unknown owner must fail closed: %v", err)
	}
}
