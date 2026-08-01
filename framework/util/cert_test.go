package util

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// TestGenerateCertInRootCreatesValidMatchingPair 验证开发证书包含现代 SAN、
// 正序列号、合理时钟偏移，并与受保护的 ECDSA 私钥严格匹配。
func TestGenerateCertInRootCreatesValidMatchingPair(t *testing.T) {
	basePath := t.TempDir()
	start := time.Now()
	if err := GenerateCertInRoot(basePath, filepath.Join("runtime", "cert.pem"), filepath.Join("runtime", "key.pem")); err != nil {
		t.Fatalf("生成开发证书失败: %v", err)
	}

	certificate := readTestCertificate(t, filepath.Join(basePath, "runtime", "cert.pem"))
	privateKey := readTestPrivateKey(t, filepath.Join(basePath, "runtime", "key.pem"))
	publicKey, ok := certificate.PublicKey.(*ecdsa.PublicKey)
	if !ok || !publicKey.Equal(privateKey.Public()) {
		t.Fatal("证书公钥与私钥不匹配")
	}
	if certificate.SerialNumber == nil || certificate.SerialNumber.Sign() <= 0 {
		t.Fatalf("证书序列号必须为正数，实际为 %v", certificate.SerialNumber)
	}
	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		if err := certificate.VerifyHostname(host); err != nil {
			t.Fatalf("证书缺少主机 %s 的 SAN: %v", host, err)
		}
	}
	if certificate.NotBefore.After(start) || certificate.NotBefore.Before(start.Add(-10*time.Minute)) {
		t.Fatalf("证书 NotBefore 未提供合理时钟偏移: %v", certificate.NotBefore)
	}
	if certificate.NotAfter.Sub(start) < 364*24*time.Hour || certificate.NotAfter.Sub(start) > 366*24*time.Hour {
		t.Fatalf("证书有效期异常: %v", certificate.NotAfter.Sub(start))
	}
	if certificate.IsCA || certificate.KeyUsage&x509.KeyUsageDigitalSignature == 0 || certificate.KeyUsage&x509.KeyUsageKeyEncipherment != 0 {
		t.Fatalf("ECDSA 服务证书用途错误: isCA=%v usage=%v", certificate.IsCA, certificate.KeyUsage)
	}
	if err := certificate.CheckSignature(certificate.SignatureAlgorithm, certificate.RawTBSCertificate, certificate.Signature); err != nil {
		t.Fatalf("自签名证书签名无效: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(basePath, "runtime", "key.pem"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("私钥权限必须为 0600: mode=%v err=%v", info.Mode().Perm(), err)
		}
	}
}

// TestGenerateCertInRootNeverOverwritesOrLeavesPartialPair 验证已有任一目标时
// 不覆盖内容，也不会留下新旧不匹配的另一半文件。
func TestGenerateCertInRootNeverOverwritesOrLeavesPartialPair(t *testing.T) {
	basePath := t.TempDir()
	certName := filepath.Join("runtime", "cert.pem")
	keyName := filepath.Join("runtime", "key.pem")
	if err := GenerateCertInRoot(basePath, certName, keyName); err != nil {
		t.Fatalf("首次生成失败: %v", err)
	}
	certBefore, _ := os.ReadFile(filepath.Join(basePath, certName))
	keyBefore, _ := os.ReadFile(filepath.Join(basePath, keyName))
	if err := GenerateCertInRoot(basePath, certName, keyName); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("重复生成应返回 fs.ErrExist，实际为 %v", err)
	}
	certAfter, _ := os.ReadFile(filepath.Join(basePath, certName))
	keyAfter, _ := os.ReadFile(filepath.Join(basePath, keyName))
	if string(certAfter) != string(certBefore) || string(keyAfter) != string(keyBefore) {
		t.Fatal("重复生成修改了已有证书或私钥")
	}

	partialRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(partialRoot, "runtime"), 0o700); err != nil {
		t.Fatalf("创建测试目录失败: %v", err)
	}
	keyPath := filepath.Join(partialRoot, keyName)
	if err := os.WriteFile(keyPath, []byte("existing-key"), 0o600); err != nil {
		t.Fatalf("写入已有私钥失败: %v", err)
	}
	if err := GenerateCertInRoot(partialRoot, certName, keyName); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("已有私钥时应返回 fs.ErrExist，实际为 %v", err)
	}
	if _, err := os.Stat(filepath.Join(partialRoot, certName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("私钥冲突后不应遗留新证书，实际错误为 %v", err)
	}
	if key, _ := os.ReadFile(keyPath); string(key) != "existing-key" {
		t.Fatalf("已有私钥被修改: %q", key)
	}
}

// TestGenerateCertInRootConfinesSymlinksAndConcurrentCreation 验证根目录外符号链接
// 被拒绝，两个并发生成请求最多只有一个完整成功。
func TestGenerateCertInRootConfinesSymlinksAndConcurrentCreation(t *testing.T) {
	basePath := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(basePath, "runtime")); err == nil {
		err := GenerateCertInRoot(basePath, filepath.Join("runtime", "cert.pem"), filepath.Join("runtime", "key.pem"))
		if err == nil {
			t.Fatal("指向根目录外的 runtime 符号链接应被拒绝")
		}
		if entries, readErr := os.ReadDir(outside); readErr != nil || len(entries) != 0 {
			t.Fatalf("根目录外不应生成证书文件: entries=%v err=%v", entries, readErr)
		}
	} else if runtime.GOOS != "windows" {
		t.Fatalf("创建符号链接失败: %v", err)
	}

	concurrentRoot := t.TempDir()
	errorsByCall := make([]error, 2)
	start := make(chan struct{})
	var waitGroup sync.WaitGroup
	for index := range errorsByCall {
		waitGroup.Add(1)
		go func(call int) {
			defer waitGroup.Done()
			<-start
			errorsByCall[call] = GenerateCertInRoot(concurrentRoot, "cert.pem", "key.pem")
		}(index)
	}
	close(start)
	waitGroup.Wait()
	successes := 0
	existsErrors := 0
	for _, err := range errorsByCall {
		if err == nil {
			successes++
		} else if errors.Is(err, fs.ErrExist) {
			existsErrors++
		} else {
			t.Fatalf("并发生成返回意外错误: %v", err)
		}
	}
	if successes != 1 || existsErrors != 1 {
		t.Fatalf("并发生成结果错误: success=%d exists=%d errors=%v", successes, existsErrors, errorsByCall)
	}
	readTestCertificate(t, filepath.Join(concurrentRoot, "cert.pem"))
	readTestPrivateKey(t, filepath.Join(concurrentRoot, "key.pem"))
}

// TestGenerateCertSupportsSeparatedAbsoluteTargets 验证兼容入口能在共同根目录内
// 安全创建分处不同子目录的绝对路径目标。
func TestGenerateCertSupportsSeparatedAbsoluteTargets(t *testing.T) {
	basePath := t.TempDir()
	certPath := filepath.Join(basePath, "certificates", "development", "cert.pem")
	keyPath := filepath.Join(basePath, "private", "development", "key.pem")
	if err := GenerateCert(certPath, keyPath); err != nil {
		t.Fatalf("通过绝对路径生成证书失败: %v", err)
	}
	certificate := readTestCertificate(t, certPath)
	privateKey := readTestPrivateKey(t, keyPath)
	publicKey, ok := certificate.PublicKey.(*ecdsa.PublicKey)
	if !ok || !publicKey.Equal(privateKey.Public()) {
		t.Fatal("兼容入口生成了不匹配的证书和私钥")
	}
}

// TestGenerateCertInRootRejectsInvalidBoundaries 验证空根目录、绝对目标、
// 同名目标及不可创建的父目录均在写入前失败。
func TestGenerateCertInRootRejectsInvalidBoundaries(t *testing.T) {
	basePath := t.TempDir()
	absoluteKey := filepath.Join(basePath, "key.pem")
	testCases := []struct {
		name     string
		basePath string
		certFile string
		keyFile  string
	}{
		{name: "empty root", basePath: "", certFile: "cert.pem", keyFile: "key.pem"},
		{name: "empty certificate", basePath: basePath, certFile: "", keyFile: "key.pem"},
		{name: "absolute key", basePath: basePath, certFile: "cert.pem", keyFile: absoluteKey},
		{name: "same target", basePath: basePath, certFile: "pair.pem", keyFile: "pair.pem"},
		{name: "missing root", basePath: filepath.Join(basePath, "missing"), certFile: "cert.pem", keyFile: "key.pem"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := GenerateCertInRoot(testCase.basePath, testCase.certFile, testCase.keyFile); err == nil {
				t.Fatal("非法证书边界应返回错误")
			}
		})
	}

	blockedRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(blockedRoot, "runtime"), []byte("not-a-directory"), 0o600); err != nil {
		t.Fatalf("创建父目录冲突文件失败: %v", err)
	}
	if err := GenerateCertInRoot(blockedRoot, filepath.Join("runtime", "cert.pem"), filepath.Join("runtime", "key.pem")); err == nil {
		t.Fatal("父路径为文件时应拒绝生成证书")
	}
	entries, err := os.ReadDir(blockedRoot)
	if err != nil || len(entries) != 1 || entries[0].Name() != "runtime" {
		t.Fatalf("失败后不应遗留证书数据: entries=%v err=%v", entries, err)
	}
}

func readTestCertificate(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取证书失败: %v", err)
	}
	block, rest := pem.Decode(content)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatalf("证书 PEM 非法: block=%#v rest=%d", block, len(rest))
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("解析证书失败: %v", err)
	}
	return certificate
}

func readTestPrivateKey(t *testing.T, path string) *ecdsa.PrivateKey {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取私钥失败: %v", err)
	}
	block, rest := pem.Decode(content)
	if block == nil || block.Type != "EC PRIVATE KEY" || len(rest) != 0 {
		t.Fatalf("私钥 PEM 非法: block=%#v rest=%d", block, len(rest))
	}
	privateKey, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("解析私钥失败: %v", err)
	}
	return privateKey
}
