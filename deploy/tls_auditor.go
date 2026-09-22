package deploy

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/config"
)

const (
	tlsMaximumCertificateBytes = 4 << 20
	tlsMaximumPrivateKeyBytes  = 1 << 20
	tlsMaximumChainLength      = 16
	tlsMinimumRSAKeyBits       = 2048
)

// TLSValidationCode 是部署工具可稳定识别的 TLS 失败类别，不包含路径和密钥等敏感数据。
type TLSValidationCode string

const (
	TLSValidationCertificateFile TLSValidationCode = "certificate_file"
	TLSValidationPrivateKeyFile  TLSValidationCode = "private_key_file"
	TLSValidationPermission      TLSValidationCode = "private_key_permission"
	TLSValidationCertificatePEM  TLSValidationCode = "certificate_pem"
	TLSValidationPrivateKeyPEM   TLSValidationCode = "private_key_pem"
	TLSValidationKeyPair         TLSValidationCode = "key_pair"
	TLSValidationNotYetValid     TLSValidationCode = "not_yet_valid"
	TLSValidationExpired         TLSValidationCode = "expired"
	TLSValidationUsage           TLSValidationCode = "certificate_usage"
	TLSValidationAlgorithm       TLSValidationCode = "certificate_algorithm"
	TLSValidationChain           TLSValidationCode = "certificate_chain"
	TLSValidationHostname        TLSValidationCode = "certificate_hostname"
	TLSValidationTarget          TLSValidationCode = "configured_target"
)

// TLSValidationError 保存机器可读类别；Error 只返回经过筛选的固定描述，避免泄露文件路径或 PEM 内容。
type TLSValidationError struct {
	Code    TLSValidationCode
	message string
}

func (validationError *TLSValidationError) Error() string {
	if validationError == nil {
		return "TLS 验证失败"
	}
	return validationError.message
}

type tlsValidationOptions struct {
	now   time.Time
	roots *x509.CertPool
}

type tlsValidationReport struct {
	certificateCount       int
	targetCount            int
	windowsACLUnverified   bool
	leafCertificateExpires time.Time
}

func checkTLSFilesWithOptions(app *framework.App, options tlsValidationOptions) Result {
	configuration, err := applicationConfig(app)
	if err != nil {
		return fail(err.Error())
	}
	if !configuration.GetBool("app.server.tls.enable") {
		return fail("生产部署未启用 TLS")
	}
	report, err := validateTLSMaterial(app, configuration, options)
	if err != nil {
		return fail(err.Error())
	}
	if report.windowsACLUnverified {
		return warn(fmt.Sprintf("TLS %d 级证书材料、私钥配对和 %d 个服务目标校验通过，叶子证书有效期至 %s；Windows ACL 需要由部署平台另行验证", report.certificateCount, report.targetCount, report.leafCertificateExpires.UTC().Format(time.RFC3339)))
	}
	return pass(fmt.Sprintf("TLS %d 级证书材料、私钥配对和 %d 个服务目标校验通过，叶子证书有效期至 %s，私钥权限受限", report.certificateCount, report.targetCount, report.leafCertificateExpires.UTC().Format(time.RFC3339)))
}

func validateTLSMaterial(app *framework.App, configuration *config.Config, options tlsValidationOptions) (tlsValidationReport, error) {
	if app == nil || configuration == nil {
		return tlsValidationReport{}, newTLSValidationError(TLSValidationCertificateFile, "TLS 应用或配置不可用", nil)
	}
	certPath := resolveApplicationPath(app.BasePath, configuration.GetString("app.server.tls.cert_file"))
	keyPath := resolveApplicationPath(app.BasePath, configuration.GetString("app.server.tls.key_file"))
	certificatePEM, _, err := readTLSRegularFile(certPath, tlsMaximumCertificateBytes, TLSValidationCertificateFile, "TLS 证书文件不可用")
	if err != nil {
		return tlsValidationReport{}, err
	}
	privateKeyPEM, keyInfo, err := readTLSRegularFile(keyPath, tlsMaximumPrivateKeyBytes, TLSValidationPrivateKeyFile, "TLS 私钥文件不可用")
	if err != nil {
		return tlsValidationReport{}, err
	}
	if runtime.GOOS != "windows" && keyInfo.Mode().Perm()&0o077 != 0 {
		return tlsValidationReport{}, newTLSValidationError(TLSValidationPermission, "TLS 私钥权限过宽", nil)
	}

	certificates, err := parseTLSCertificateBundle(certificatePEM)
	if err != nil {
		return tlsValidationReport{}, err
	}
	privateKey, err := parseTLSPrivateKey(privateKeyPEM)
	if err != nil {
		return tlsValidationReport{}, err
	}
	if err := validateTLSKeyPair(certificates[0], privateKey); err != nil {
		return tlsValidationReport{}, err
	}

	now := options.now
	if now.IsZero() {
		now = time.Now()
	}
	leaf := certificates[0]
	if now.Before(leaf.NotBefore) {
		return tlsValidationReport{}, newTLSValidationError(TLSValidationNotYetValid, "TLS 证书尚未生效", nil)
	}
	if !now.Before(leaf.NotAfter) {
		return tlsValidationReport{}, newTLSValidationError(TLSValidationExpired, "TLS 证书已经过期", nil)
	}
	if !certificateAllowsServerAuthentication(leaf) {
		return tlsValidationReport{}, newTLSValidationError(TLSValidationUsage, "TLS 证书未授权服务端身份认证", nil)
	}
	for _, certificate := range certificates {
		if err := validateTLSCertificateAlgorithm(certificate); err != nil {
			return tlsValidationReport{}, err
		}
	}
	if err := verifyTLSCertificateChain(certificates, now, options.roots); err != nil {
		return tlsValidationReport{}, err
	}
	targets, err := configuredTLSTargets(configuration)
	if err != nil {
		return tlsValidationReport{}, err
	}
	for _, target := range targets {
		if err := verifyTLSCertificateTarget(leaf, target); err != nil {
			return tlsValidationReport{}, err
		}
	}
	return tlsValidationReport{
		certificateCount:       len(certificates),
		targetCount:            len(targets),
		windowsACLUnverified:   runtime.GOOS == "windows",
		leafCertificateExpires: leaf.NotAfter,
	}, nil
}

func readTLSRegularFile(path string, maximumBytes int64, code TLSValidationCode, message string) ([]byte, os.FileInfo, error) {
	cleaned := filepath.Clean(path)
	root, err := os.OpenRoot(filepath.Dir(cleaned))
	if err != nil {
		return nil, nil, newTLSValidationError(code, message, err)
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(cleaned)
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maximumBytes {
		return nil, nil, newTLSValidationError(code, message, err)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, newTLSValidationError(code, message, err)
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Size() <= 0 || openedInfo.Size() > maximumBytes || !os.SameFile(info, openedInfo) {
		return nil, nil, newTLSValidationError(code, message, err)
	}
	currentPathInfo, err := root.Lstat(name)
	if err != nil || !currentPathInfo.Mode().IsRegular() || currentPathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(currentPathInfo, openedInfo) {
		return nil, nil, newTLSValidationError(code, message, err)
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil || int64(len(content)) > maximumBytes {
		return nil, nil, newTLSValidationError(code, message, err)
	}
	return content, openedInfo, nil
}

func parseTLSCertificateBundle(content []byte) ([]*x509.Certificate, error) {
	remaining := bytes.TrimSpace(content)
	certificates := make([]*x509.Certificate, 0, 2)
	for len(remaining) > 0 {
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, newTLSValidationError(TLSValidationCertificatePEM, "TLS 证书 PEM 格式非法", nil)
		}
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, newTLSValidationError(TLSValidationCertificatePEM, "TLS 证书 PEM 格式非法", nil)
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, newTLSValidationError(TLSValidationCertificatePEM, "TLS 证书 PEM 格式非法", err)
		}
		certificates = append(certificates, certificate)
		if len(certificates) > tlsMaximumChainLength {
			return nil, newTLSValidationError(TLSValidationCertificatePEM, "TLS 证书链长度超过安全上限", nil)
		}
		remaining = bytes.TrimSpace(rest)
	}
	if len(certificates) == 0 {
		return nil, newTLSValidationError(TLSValidationCertificatePEM, "TLS 证书 PEM 格式非法", nil)
	}
	return certificates, nil
}

func parseTLSPrivateKey(content []byte) (crypto.Signer, error) {
	remaining := bytes.TrimSpace(content)
	if !bytes.HasPrefix(remaining, []byte("-----BEGIN ")) {
		return nil, newTLSValidationError(TLSValidationPrivateKeyPEM, "TLS 私钥 PEM 格式非法", nil)
	}
	block, rest := pem.Decode(remaining)
	if block == nil || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, newTLSValidationError(TLSValidationPrivateKeyPEM, "TLS 私钥 PEM 格式非法", nil)
	}
	var parsed any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		parsed, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, newTLSValidationError(TLSValidationPrivateKeyPEM, "TLS 私钥 PEM 类型不受支持", nil)
	}
	if err != nil {
		return nil, newTLSValidationError(TLSValidationPrivateKeyPEM, "TLS 私钥 PEM 格式非法", err)
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, newTLSValidationError(TLSValidationPrivateKeyPEM, "TLS 私钥算法不受支持", nil)
	}
	return signer, nil
}

func validateTLSKeyPair(certificate *x509.Certificate, privateKey crypto.Signer) error {
	certificatePublic, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil {
		return newTLSValidationError(TLSValidationKeyPair, "TLS 证书公钥无法解析", err)
	}
	privatePublic, err := x509.MarshalPKIXPublicKey(privateKey.Public())
	if err != nil || !bytes.Equal(certificatePublic, privatePublic) {
		return newTLSValidationError(TLSValidationKeyPair, "TLS 证书与私钥不匹配", err)
	}
	return nil
}

func certificateAllowsServerAuthentication(certificate *x509.Certificate) bool {
	if len(certificate.ExtKeyUsage) == 0 {
		return true
	}
	for _, usage := range certificate.ExtKeyUsage {
		if usage == x509.ExtKeyUsageAny || usage == x509.ExtKeyUsageServerAuth {
			return true
		}
	}
	return false
}

func validateTLSCertificateAlgorithm(certificate *x509.Certificate) error {
	switch certificate.SignatureAlgorithm {
	case x509.MD2WithRSA, x509.MD5WithRSA, x509.SHA1WithRSA, x509.DSAWithSHA1, x509.DSAWithSHA256, x509.ECDSAWithSHA1, x509.UnknownSignatureAlgorithm:
		return newTLSValidationError(TLSValidationAlgorithm, "TLS 证书签名算法不符合安全要求", nil)
	}
	switch publicKey := certificate.PublicKey.(type) {
	case *rsa.PublicKey:
		if publicKey.N == nil || publicKey.N.BitLen() < tlsMinimumRSAKeyBits {
			return newTLSValidationError(TLSValidationAlgorithm, "TLS RSA 公钥长度不足", nil)
		}
	case *ecdsa.PublicKey:
		if publicKey.Curve == nil || publicKey.Curve.Params() == nil || publicKey.Curve.Params().BitSize < 256 {
			return newTLSValidationError(TLSValidationAlgorithm, "TLS ECDSA 曲线强度不足", nil)
		}
	case ed25519.PublicKey:
		if len(publicKey) != ed25519.PublicKeySize {
			return newTLSValidationError(TLSValidationAlgorithm, "TLS Ed25519 公钥格式非法", nil)
		}
	default:
		return newTLSValidationError(TLSValidationAlgorithm, "TLS 证书公钥算法不受支持", nil)
	}
	return nil
}

func verifyTLSCertificateChain(certificates []*x509.Certificate, now time.Time, roots *x509.CertPool) error {
	if roots == nil {
		var err error
		roots, err = x509.SystemCertPool()
		if err != nil || roots == nil {
			return newTLSValidationError(TLSValidationChain, "TLS 系统信任根不可用", err)
		}
	}
	intermediates := x509.NewCertPool()
	for _, certificate := range certificates[1:] {
		intermediates.AddCert(certificate)
	}
	_, err := certificates[0].Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	if err != nil {
		return newTLSValidationError(TLSValidationChain, "TLS 证书链无法建立到受信根", err)
	}
	return nil
}

func configuredTLSTargets(configuration *config.Config) ([]string, error) {
	targets := make([]string, 0, 4)
	domain := strings.TrimSpace(configuration.GetString("app.server.domain"))
	if domain != "" {
		parsed, err := url.Parse(domain)
		if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" || parsed.User != nil {
			return nil, newTLSValidationError(TLSValidationTarget, "TLS 服务主域名必须是有效的 HTTPS URL", err)
		}
		targets = append(targets, parsed.Hostname())
	}
	hosts, err := stringList(configuration.Get("app.server.allowed_hosts"))
	if err != nil {
		return nil, newTLSValidationError(TLSValidationTarget, "TLS Host 白名单格式非法", err)
	}
	targets = append(targets, hosts...)
	seen := make(map[string]struct{}, len(targets))
	normalized := make([]string, 0, len(targets))
	for _, target := range targets {
		target, err = normalizeTLSTarget(target)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[target]; exists {
			continue
		}
		seen[target] = struct{}{}
		normalized = append(normalized, target)
	}
	if len(normalized) == 0 {
		return nil, newTLSValidationError(TLSValidationTarget, "TLS 服务目标不能为空", nil)
	}
	return normalized, nil
}

func normalizeTLSTarget(target string) (string, error) {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" || target == "*" || strings.ContainsAny(target, "/\\@?#\x00\r\n\t ") {
		return "", newTLSValidationError(TLSValidationTarget, "TLS 服务目标格式非法", nil)
	}
	wildcard := strings.HasPrefix(target, "*.")
	candidate := strings.TrimPrefix(target, "*.")
	host, err := tlsTargetHostWithoutPort(candidate)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(host); ip != nil {
		if wildcard {
			return "", newTLSValidationError(TLSValidationTarget, "TLS IP 目标不能使用通配符", nil)
		}
		return ip.String(), nil
	}
	if !validTLSDNSName(host) {
		return "", newTLSValidationError(TLSValidationTarget, "TLS 服务目标格式非法", nil)
	}
	if wildcard {
		return "*." + host, nil
	}
	return host, nil
}

func tlsTargetHostWithoutPort(candidate string) (string, error) {
	if strings.HasPrefix(candidate, "[") {
		closingBracket := strings.IndexByte(candidate, ']')
		if closingBracket <= 1 {
			return "", newTLSValidationError(TLSValidationTarget, "TLS 服务目标格式非法", nil)
		}
		host := candidate[1:closingBracket]
		remainder := candidate[closingBracket+1:]
		if net.ParseIP(host) == nil || remainder != "" && (!strings.HasPrefix(remainder, ":") || !validTLSPort(remainder[1:])) {
			return "", newTLSValidationError(TLSValidationTarget, "TLS 服务目标格式非法", nil)
		}
		return host, nil
	}
	if strings.ContainsAny(candidate, "[]") {
		return "", newTLSValidationError(TLSValidationTarget, "TLS 服务目标格式非法", nil)
	}
	if net.ParseIP(candidate) != nil {
		return candidate, nil
	}
	if strings.Count(candidate, ":") > 1 {
		return "", newTLSValidationError(TLSValidationTarget, "TLS 服务目标格式非法", nil)
	}
	if host, port, hasPort := strings.Cut(candidate, ":"); hasPort {
		if !validTLSPort(port) {
			return "", newTLSValidationError(TLSValidationTarget, "TLS 服务目标格式非法", nil)
		}
		candidate = host
	}
	return strings.TrimSuffix(candidate, "."), nil
}

func validTLSPort(port string) bool {
	if port == "" {
		return false
	}
	for _, character := range port {
		if character < '0' || character > '9' {
			return false
		}
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	return err == nil && parsed > 0
}

func validTLSDNSName(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func verifyTLSCertificateTarget(certificate *x509.Certificate, target string) error {
	if strings.HasPrefix(target, "*.") {
		for _, dnsName := range certificate.DNSNames {
			if strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(dnsName), "."), target) {
				return nil
			}
		}
		return newTLSValidationError(TLSValidationHostname, "TLS 证书 SAN 未覆盖全部配置目标", nil)
	}
	if err := certificate.VerifyHostname(target); err != nil {
		return newTLSValidationError(TLSValidationHostname, "TLS 证书 SAN 未覆盖全部配置目标", err)
	}
	return nil
}

func newTLSValidationError(code TLSValidationCode, message string, _ error) error {
	// 底层文件和解析错误可能携带绝对路径或材料片段，因此只保留稳定类别和安全描述。
	return &TLSValidationError{Code: code, message: message}
}
