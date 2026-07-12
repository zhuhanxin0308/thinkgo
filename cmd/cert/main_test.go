package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunCertCreatesPairAndRefusesOverwrite 验证证书命令创建完整文件对，
// 且重复执行不会覆盖已存在的开发密钥材料。
func TestRunCertCreatesPairAndRefusesOverwrite(t *testing.T) {
	basePath := t.TempDir()
	var output bytes.Buffer
	if err := runCert(basePath, &output); err != nil {
		t.Fatalf("执行证书命令失败: %v", err)
	}
	if !strings.Contains(output.String(), filepath.Join("runtime", "cert.pem")) ||
		!strings.Contains(output.String(), filepath.Join("runtime", "key.pem")) {
		t.Fatalf("成功输出缺少证书路径: %q", output.String())
	}

	certPath := filepath.Join(basePath, "runtime", "cert.pem")
	keyPath := filepath.Join(basePath, "runtime", "key.pem")
	certBefore, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("读取证书失败: %v", err)
	}
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("读取私钥失败: %v", err)
	}
	if err := runCert(basePath, &bytes.Buffer{}); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("重复执行应返回 fs.ErrExist，实际为 %v", err)
	}
	certAfter, _ := os.ReadFile(certPath)
	keyAfter, _ := os.ReadFile(keyPath)
	if !bytes.Equal(certAfter, certBefore) || !bytes.Equal(keyAfter, keyBefore) {
		t.Fatal("重复执行覆盖了已有证书或私钥")
	}
}

// TestRunCertPropagatesRootAndOutputErrors 验证文件系统与标准输出错误均向调用方返回。
func TestRunCertPropagatesRootAndOutputErrors(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "missing")
	if err := runCert(missingRoot, &bytes.Buffer{}); err == nil {
		t.Fatal("不存在的项目根目录应返回错误")
	}
	if err := runCert(t.TempDir(), failingCertWriter{}); err == nil {
		t.Fatal("成功信息写入失败时应返回错误")
	}
	if err := runCert(t.TempDir(), nil); err == nil {
		t.Fatal("空标准输出应返回错误")
	}
}

type failingCertWriter struct{}

func (failingCertWriter) Write([]byte) (int, error) {
	return 0, errors.New("测试输出失败")
}
