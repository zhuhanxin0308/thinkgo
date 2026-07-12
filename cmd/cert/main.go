package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"thinkgo/framework/util"
)

func main() {
	if err := runCert(".", os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "生成开发证书失败: %v\n", err)
		os.Exit(1)
	}
}

// runCert 在项目根目录内生成默认开发证书，并将结果写入调用方提供的输出流。
func runCert(basePath string, stdout io.Writer) error {
	if stdout == nil {
		return fmt.Errorf("标准输出不能为空")
	}
	certFile := filepath.Join("runtime", "cert.pem")
	keyFile := filepath.Join("runtime", "key.pem")
	if err := util.GenerateCertInRoot(basePath, certFile, keyFile); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "开发证书已生成：%s，私钥：%s\n", certFile, keyFile); err != nil {
		return fmt.Errorf("输出证书路径失败: %w", err)
	}
	return nil
}
