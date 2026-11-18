package util

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const developmentCertificateClockSkew = 5 * time.Minute

// GenerateCert 在证书和私钥的共同父目录约束内生成开发证书。
// 已存在任一目标时返回 fs.ErrExist，禁止静默轮换成不匹配的文件对。
func GenerateCert(certFile, keyFile string) error {
	if err := validateCertificateTargetPath(certFile, "证书"); err != nil {
		return err
	}
	if err := validateCertificateTargetPath(keyFile, "私钥"); err != nil {
		return err
	}
	if !filepath.IsAbs(certFile) || !filepath.IsAbs(keyFile) {
		if err := validateCertificateWorkingDirectory(); err != nil {
			return fmt.Errorf("验证证书工作目录失败: %w", err)
		}
	}
	certAbsolute, err := filepath.Abs(certFile)
	if err != nil {
		return fmt.Errorf("解析证书路径失败: %w", err)
	}
	keyAbsolute, err := filepath.Abs(keyFile)
	if err != nil {
		return fmt.Errorf("解析私钥路径失败: %w", err)
	}
	if err := validateCertificatePairPaths(certAbsolute, keyAbsolute); err != nil {
		return err
	}
	rootPath, err := commonCertificateRoot(filepath.Dir(certAbsolute), keyAbsolute)
	if err != nil {
		return err
	}
	certRelative, err := filepath.Rel(rootPath, certAbsolute)
	if err != nil {
		return fmt.Errorf("解析证书相对路径失败: %w", err)
	}
	keyRelative, err := filepath.Rel(rootPath, keyAbsolute)
	if err != nil {
		return fmt.Errorf("解析私钥相对路径失败: %w", err)
	}
	return GenerateCertInRoot(rootPath, certRelative, keyRelative)
}

// validateCertificateWorkingDirectory 核实相对目标依赖的工作目录仍可按路径访问。
// 某些系统可能返回失效的目录路径，必须在创建共同根目录的子目录前拒绝它。
func validateCertificateWorkingDirectory() error {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return err
	}
	currentInfo, err := os.Stat(".")
	if err != nil {
		return err
	}
	workingInfo, err := os.Stat(workingDirectory)
	if err != nil {
		return err
	}
	if !os.SameFile(currentInfo, workingInfo) {
		return &os.PathError{Op: "stat", Path: workingDirectory, Err: fs.ErrNotExist}
	}
	return nil
}

// GenerateCertInRoot 在指定根目录中成对独占创建自签名 ECDSA 开发证书和私钥。
// os.Root 会拒绝路径穿越、绝对路径和指向根目录外的符号链接。
func GenerateCertInRoot(basePath, certFile, keyFile string) (returnErr error) {
	if strings.TrimSpace(basePath) == "" {
		return fmt.Errorf("证书根目录不能为空")
	}
	if err := validateCertificateTargetPath(certFile, "证书"); err != nil {
		return err
	}
	if err := validateCertificateTargetPath(keyFile, "私钥"); err != nil {
		return err
	}
	certFile = filepath.Clean(certFile)
	keyFile = filepath.Clean(keyFile)
	if certFile == "." || keyFile == "." || filepath.IsAbs(certFile) || filepath.IsAbs(keyFile) || !filepath.IsLocal(certFile) || !filepath.IsLocal(keyFile) {
		return fmt.Errorf("证书和私钥必须是根目录内的相对文件路径")
	}
	if err := validateCertificatePairPaths(certFile, keyFile); err != nil {
		return err
	}

	certificatePEM, privateKeyPEM, err := generateDevelopmentCertificate(time.Now())
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(basePath)
	if err != nil {
		return fmt.Errorf("打开证书根目录失败: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	createdDirs := make([]string, 0, 2)
	complete := false
	defer func() {
		if complete {
			return
		}
		for index := len(createdDirs) - 1; index >= 0; index-- {
			_ = removeRootFile(root, createdDirs[index])
		}
	}()
	for _, directory := range []string{filepath.Dir(certFile), filepath.Dir(keyFile)} {
		created, err := ensureCertificateRootDirectory(root, directory)
		createdDirs = append(createdDirs, created...)
		if err != nil {
			return fmt.Errorf("创建证书目录失败: %w", err)
		}
	}

	certHandle, err := root.OpenFile(certFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("创建证书文件失败: %w", err)
	}
	keyHandle, err := root.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		closeErr := certHandle.Close()
		removeErr := removeRootFile(root, certFile)
		return errors.Join(fmt.Errorf("创建私钥文件失败: %w", err), closeErr, removeErr)
	}
	defer func() {
		if complete {
			return
		}
		returnErr = errors.Join(returnErr, certHandle.Close(), keyHandle.Close(),
			removeRootFile(root, certFile), removeRootFile(root, keyFile))
	}()

	// 在写入私钥内容前限制当前句柄权限，避免继承的宽松 ACL 产生短暂泄露窗口。
	if err := restrictPrivateFile(keyHandle); err != nil {
		return fmt.Errorf("限制私钥权限失败: %w", err)
	}
	if err := writeAndSyncFile(certHandle, certificatePEM); err != nil {
		return fmt.Errorf("写入证书失败: %w", err)
	}
	if err := writeAndSyncFile(keyHandle, privateKeyPEM); err != nil {
		return fmt.Errorf("写入私钥失败: %w", err)
	}
	if err := certHandle.Close(); err != nil {
		return fmt.Errorf("关闭证书文件失败: %w", err)
	}
	if err := keyHandle.Close(); err != nil {
		return fmt.Errorf("关闭私钥文件失败: %w", err)
	}
	complete = true
	return nil
}

func generateDevelopmentCertificate(now time.Time) ([]byte, []byte, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("生成 ECDSA 私钥失败: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	var serialNumber *big.Int
	for serialNumber == nil || serialNumber.Sign() <= 0 {
		serialNumber, err = rand.Int(rand.Reader, serialLimit)
		if err != nil {
			return nil, nil, fmt.Errorf("生成证书序列号失败: %w", err)
		}
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"ThinkGo Development"},
			CommonName:   "localhost",
		},
		NotBefore: now.Add(-developmentCertificateClockSkew),
		NotAfter:  now.AddDate(1, 0, 0),

		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("创建自签名证书失败: %w", err)
	}
	privateKeyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("编码 ECDSA 私钥失败: %w", err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateKeyDER})
	if len(certificatePEM) == 0 || len(privateKeyPEM) == 0 {
		return nil, nil, fmt.Errorf("编码证书 PEM 失败")
	}
	return certificatePEM, privateKeyPEM, nil
}

func writeAndSyncFile(file *os.File, content []byte) error {
	written, err := file.Write(content)
	if err == nil && written != len(content) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return err
	}
	return file.Sync()
}

func removeRootFile(root *os.Root, name string) error {
	err := root.Remove(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func validateCertificateTargetPath(rawPath, label string) error {
	if strings.TrimSpace(rawPath) == "" {
		return fmt.Errorf("%s文件路径不能为空", label)
	}
	for _, character := range rawPath {
		if character < ' ' || character == 0x7f {
			return fmt.Errorf("%s文件路径不能包含控制字符", label)
		}
	}
	if strings.HasSuffix(rawPath, "/") || strings.HasSuffix(rawPath, "\\") {
		return fmt.Errorf("%s文件路径不能以目录分隔符结尾", label)
	}
	volume := filepath.VolumeName(rawPath)
	withoutVolume := rawPath[len(volume):]
	for _, segment := range strings.FieldsFunc(withoutVolume, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if strings.TrimSpace(segment) == "" {
			return fmt.Errorf("%s文件路径不能包含全空白名称", label)
		}
	}
	cleaned := filepath.Clean(rawPath)
	base := filepath.Base(cleaned)
	if cleaned == "." || cleaned == ".." || base == "." || base == ".." || base == string(filepath.Separator) || strings.TrimSpace(base) == "" {
		return fmt.Errorf("%s文件路径必须指向具体文件", label)
	}
	return nil
}

func validateCertificatePairPaths(certPath, keyPath string) error {
	if certPath == keyPath {
		return fmt.Errorf("证书和私钥不能使用同一路径")
	}
	if isCertificatePathAncestor(certPath, keyPath) || isCertificatePathAncestor(keyPath, certPath) {
		return fmt.Errorf("证书和私钥路径不能互为父子目录")
	}
	return nil
}

func isCertificatePathAncestor(ancestor, descendant string) bool {
	relative, err := filepath.Rel(ancestor, descendant)
	if err != nil {
		return false
	}
	return relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func ensureCertificateRootDirectory(root *os.Root, directory string) ([]string, error) {
	cleaned := filepath.Clean(directory)
	if cleaned == "." || cleaned == "" {
		return nil, nil
	}
	segments := strings.Split(cleaned, string(filepath.Separator))
	created := make([]string, 0, len(segments))
	current := ""
	for _, segment := range segments {
		if segment == "" || segment == "." {
			continue
		}
		if current == "" {
			current = segment
		} else {
			current = filepath.Join(current, segment)
		}
		info, err := root.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return created, &os.PathError{Op: "mkdir", Path: current, Err: fs.ErrExist}
			}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return created, err
		}
		if err := root.Mkdir(current, 0o700); err != nil {
			if errors.Is(err, fs.ErrExist) {
				if statInfo, statErr := root.Stat(current); statErr == nil && statInfo.IsDir() {
					continue
				}
			}
			return created, err
		}
		created = append(created, current)
	}
	return created, nil
}

func commonCertificateRoot(certDirectory, keyAbsolute string) (string, error) {
	if !strings.EqualFold(filepath.VolumeName(certDirectory), filepath.VolumeName(keyAbsolute)) {
		return "", fmt.Errorf("证书和私钥必须位于同一文件系统卷")
	}
	current := filepath.Clean(certDirectory)
	for {
		relative, err := filepath.Rel(current, keyAbsolute)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return "", fmt.Errorf("无法确定证书和私钥的共同根目录")
}
