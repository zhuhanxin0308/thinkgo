//go:build !windows

package util

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestGenerateCertWithRemovedWorkingDirectory 验证发布目录被移除后，
// 任一相对目标均明确失败，且不会重建失效目录或留下不完整证书。
func TestGenerateCertWithRemovedWorkingDirectory(t *testing.T) {
	testCases := []struct {
		name                string
		absoluteCertificate bool
		absolutePrivateKey  bool
	}{
		{name: "两个相对目标"},
		{name: "绝对证书和相对私钥", absoluteCertificate: true},
		{name: "相对证书和绝对私钥", absolutePrivateKey: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			output := t.TempDir()
			directory := t.TempDir()
			certificate := "cert.pem"
			privateKey := "key.pem"
			if testCase.absoluteCertificate {
				certificate = filepath.Join(output, certificate)
			}
			if testCase.absolutePrivateKey {
				privateKey = filepath.Join(output, privateKey)
			}
			t.Chdir(directory)
			if err := os.Remove(directory); err != nil {
				t.Fatal(err)
			}
			if err := GenerateCert(certificate, privateKey); !errors.Is(err, fs.ErrNotExist) {
				workingDirectory, workingDirectoryErr := os.Getwd()
				t.Fatalf("丢失工作目录应明确失败: %v；当前目录=%q，目录解析错误=%v", err, workingDirectory, workingDirectoryErr)
			}
			if _, err := os.Stat(directory); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("不应重建已移除的工作目录: %v", err)
			}
			if entries, err := os.ReadDir(output); err != nil || len(entries) != 0 {
				t.Fatalf("失败留下了证书: %v %v", entries, err)
			}
		})
	}
}

// TestGenerateCertWithAbsoluteTargetsIgnoresRemovedWorkingDirectory 验证纯绝对目标
// 不依赖已移除的工作目录，并生成完整的证书与私钥。
func TestGenerateCertWithAbsoluteTargetsIgnoresRemovedWorkingDirectory(t *testing.T) {
	output := t.TempDir()
	directory := t.TempDir()
	certificate := filepath.Join(output, "cert.pem")
	privateKey := filepath.Join(output, "key.pem")
	t.Chdir(directory)
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err := GenerateCert(certificate, privateKey); err != nil {
		t.Fatalf("纯绝对目标不应依赖已移除的工作目录: %v", err)
	}
	if _, err := os.Stat(directory); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("不应重建已移除的工作目录: %v", err)
	}
	readTestCertificate(t, certificate)
	readTestPrivateKey(t, privateKey)
}

// TestGenerateCertUsesRenamedWorkingDirectory 验证工作目录重命名且旧路径被占用时，
// 相对目标仍写入当前目录对象，不会误写到环境变量中的旧路径。
func TestGenerateCertUsesRenamedWorkingDirectory(t *testing.T) {
	basePath := t.TempDir()
	original := filepath.Join(basePath, "original")
	renamed := filepath.Join(basePath, "renamed")
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(original)
	if err := os.Rename(original, renamed); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := GenerateCert("cert.pem", "key.pem"); err != nil {
		t.Fatalf("重命名后的工作目录应仍然有效: %v", err)
	}
	readTestCertificate(t, filepath.Join(renamed, "cert.pem"))
	readTestPrivateKey(t, filepath.Join(renamed, "key.pem"))
	if entries, err := os.ReadDir(original); err != nil || len(entries) != 0 {
		t.Fatalf("旧路径不应生成证书: %v %v", entries, err)
	}
}

// TestGenerateCertRejectsUnsearchableWorkingDirectory 验证目录路径仍可被系统解析，
// 但当前目录已禁止遍历时，混合目标应在创建任何文件前返回权限错误。
func TestGenerateCertRejectsUnsearchableWorkingDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 会绕过目录权限，无法验证普通用户的访问边界")
	}
	output := t.TempDir()
	directory := t.TempDir()
	t.Chdir(directory)
	// 清空 PWD 后由系统解析当前路径，避免环境变量快捷路径提前触发权限检查。
	t.Setenv("PWD", "")
	t.Cleanup(func() {
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Errorf("恢复工作目录权限失败: %v", err)
		}
	})
	if err := os.Chmod(directory, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := GenerateCert(filepath.Join(output, "cert.pem"), "key.pem"); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("无法遍历工作目录时应明确返回权限错误: %v", err)
	}
	if entries, err := os.ReadDir(output); err != nil || len(entries) != 0 {
		t.Fatalf("权限检查失败不应留下证书: %v %v", entries, err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(directory); err != nil || len(entries) != 0 {
		t.Fatalf("权限检查失败不应留下私钥: %v %v", entries, err)
	}
}
