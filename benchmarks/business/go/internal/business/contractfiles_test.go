package business

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestBoundedContractReadLimitsBytes 覆盖正常合同与超预算读取，避免把大文件整份载入内存。
func TestBoundedContractReadLimitsBytes(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "dataset.json")
	for _, payload := range [][]byte{[]byte("contract"), bytes.Repeat([]byte("x"), maximumConfigBytes+8)} {
		if err := os.WriteFile(path, payload, 0600); err != nil {
			t.Fatal(err)
		}
		read, err := readBoundedContractFile(path)
		if err != nil {
			t.Fatal(err)
		}
		expected := len(payload)
		if expected > maximumConfigBytes+1 {
			expected = maximumConfigBytes + 1
		}
		if len(read) != expected || !bytes.Equal(read, payload[:expected]) {
			t.Fatalf("合同读取大小或内容异常: got=%d expected=%d", len(read), expected)
		}
	}
}

// TestBoundedContractReadRejectsEscapingSymlink 保证合同文件不能通过符号链接跳出其明确的配置目录。
func TestBoundedContractReadRejectsEscapingSymlink(t *testing.T) {
	directory := t.TempDir()
	allowed := filepath.Join(directory, "allowed")
	if err := os.Mkdir(allowed, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(directory, "outside.json")
	if err := os.WriteFile(outside, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(allowed, "dataset.json")
	if err := os.Symlink("../outside.json", link); err != nil {
		t.Skipf("当前平台无法创建符号链接: %v", err)
	}
	if _, err := readBoundedContractFile(link); err == nil {
		t.Fatal("允许合同文件符号链接越过配置目录")
	}
}

// TestBoundedContractReadRejectsInvalidBasename 拒绝把目录或空路径误当作 JSON 文件读取。
func TestBoundedContractReadRejectsInvalidBasename(t *testing.T) {
	for _, path := range []string{"", ".", "..", "contracts/"} {
		if _, err := readBoundedContractFile(path); err == nil {
			t.Errorf("接受了非法合同文件路径: %q", path)
		}
	}
}
