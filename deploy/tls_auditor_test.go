package deploy

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/config"
)

type tlsTestLeafOptions struct {
	notBefore   time.Time
	notAfter    time.Time
	dnsNames    []string
	ipAddresses []net.IP
	key         crypto.Signer
	usages      []x509.ExtKeyUsage
}

type tlsTestPKI struct {
	now             time.Time
	rootCertificate *x509.Certificate
	rootPool        *x509.CertPool
	intermediateDER []byte
	leafDER         []byte
	leafKey         crypto.Signer
}

// TestTLSValidationAcceptsTrustedChainAndAllConfiguredHosts 验证真实 CA 链、域名、通配域名和 IP SAN 的完整成功路径。
func TestTLSValidationAcceptsTrustedChainAndAllConfiguredHosts(t *testing.T) {
	now := time.Date(2026, time.August, 15, 8, 0, 0, 0, time.UTC)
	pki := newTLSTestPKI(t, now, tlsTestLeafOptions{
		dnsNames:    []string{"api.example.com", "*.tenant.example.com"},
		ipAddresses: []net.IP{net.ParseIP("192.0.2.10")},
	})
	app, configuration := newDeployTestApp(t)
	certPath, keyPath := writeTLSTestMaterial(t, app.BasePath, pki.leafKey, 0o600, pki.leafDER, pki.intermediateDER)
	configureTLSTestApp(configuration, certPath, keyPath)

	result := checkTLSFilesWithOptions(app, tlsValidationOptions{now: now, roots: pki.rootPool})
	want := LevelPass
	if runtime.GOOS == "windows" {
		want = LevelWarn
	}
	if result.Level != want {
		t.Fatalf("可信 TLS 证书链应通过门禁: want=%s result=%#v", want, result)
	}
	if !strings.Contains(result.Message, "3") {
		t.Fatalf("成功结果应报告已校验的目标数量: %#v", result)
	}
}

// TestTLSValidationRejectsMalformedMaterialAndRedactsDetails 验证类型化错误不会暴露敏感文件路径或内容。
func TestTLSValidationRejectsMalformedMaterialAndRedactsDetails(t *testing.T) {
	app, configuration := newDeployTestApp(t)
	secretMarker := "deployment-secret-marker"
	certPath := filepath.Join(app.BasePath, secretMarker+"-cert.pem")
	keyPath := filepath.Join(app.BasePath, secretMarker+"-key.pem")
	if err := os.WriteFile(certPath, []byte("not-a-certificate-"+secretMarker), 0o644); err != nil {
		t.Fatalf("写入伪造证书失败: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte("not-a-private-key-"+secretMarker), 0o600); err != nil {
		t.Fatalf("写入伪造私钥失败: %v", err)
	}
	configureTLSTestApp(configuration, certPath, keyPath)

	_, err := validateTLSMaterial(app, configuration, tlsValidationOptions{now: time.Now(), roots: x509.NewCertPool()})
	assertTLSValidationCode(t, err, TLSValidationCertificatePEM)
	for _, forbidden := range []string{secretMarker, certPath, keyPath, "not-a-private-key"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("TLS 验证错误泄露敏感信息 %q: %v", forbidden, err)
		}
	}
}

// TestTLSRegularFileRejectsSymbolicLink 验证证书和私钥读取边界不会跟随
// 可在检查与打开之间被替换的符号链接。
func TestTLSRegularFileRejectsSymbolicLink(t *testing.T) {
	externalPath := filepath.Join(t.TempDir(), "external.pem")
	if err := os.WriteFile(externalPath, []byte("external-certificate"), 0o600); err != nil {
		t.Fatalf("写入外部 TLS 文件失败: %v", err)
	}
	linkedPath := filepath.Join(t.TempDir(), "linked.pem")
	if err := os.Symlink(externalPath, linkedPath); err != nil {
		t.Skipf("当前环境不能创建 TLS 文件符号链接: %v", err)
	}
	if _, _, err := readTLSRegularFile(linkedPath, tlsMaximumCertificateBytes, TLSValidationCertificateFile, "TLS 证书文件不可用"); err == nil {
		t.Fatal("TLS 普通文件读取必须拒绝符号链接")
	}
}

// TestTLSValidationRejectsPrivateKeyMismatch 验证证书和私钥必须来自同一密钥对。
func TestTLSValidationRejectsPrivateKeyMismatch(t *testing.T) {
	now := time.Now().UTC()
	pki := newTLSTestPKI(t, now, tlsTestLeafOptions{dnsNames: []string{"api.example.com", "*.tenant.example.com"}, ipAddresses: []net.IP{net.ParseIP("192.0.2.10")}})
	wrongKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成错配私钥失败: %v", err)
	}
	app, configuration := newDeployTestApp(t)
	certPath, keyPath := writeTLSTestMaterial(t, app.BasePath, wrongKey, 0o600, pki.leafDER, pki.intermediateDER)
	configureTLSTestApp(configuration, certPath, keyPath)

	_, validationErr := validateTLSMaterial(app, configuration, tlsValidationOptions{now: now, roots: pki.rootPool})
	assertTLSValidationCode(t, validationErr, TLSValidationKeyPair)
}

// TestTLSValidationRejectsMalformedPrivateKey 验证真实证书配合伪造私钥时返回私钥 PEM 类别，而不是模糊链错误。
func TestTLSValidationRejectsMalformedPrivateKey(t *testing.T) {
	now := time.Now().UTC()
	pki := newTLSTestPKI(t, now, tlsTestLeafOptions{dnsNames: []string{"api.example.com", "*.tenant.example.com"}, ipAddresses: []net.IP{net.ParseIP("192.0.2.10")}})
	app, configuration := newDeployTestApp(t)
	certPath, keyPath := writeTLSTestMaterial(t, app.BasePath, pki.leafKey, 0o600, pki.leafDER, pki.intermediateDER)
	if err := os.WriteFile(keyPath, []byte("not-a-private-key"), 0o600); err != nil {
		t.Fatalf("写入伪造私钥失败: %v", err)
	}
	configureTLSTestApp(configuration, certPath, keyPath)
	_, err := validateTLSMaterial(app, configuration, tlsValidationOptions{now: now, roots: pki.rootPool})
	assertTLSValidationCode(t, err, TLSValidationPrivateKeyPEM)
}

// TestTLSValidationRejectsInvalidValidityWindows 验证尚未生效和已经过期的叶子证书都会阻断部署。
func TestTLSValidationRejectsInvalidValidityWindows(t *testing.T) {
	now := time.Date(2026, time.August, 15, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		notBefore  time.Time
		notAfter   time.Time
		wantedCode TLSValidationCode
	}{
		{name: "尚未生效", notBefore: now.Add(time.Hour), notAfter: now.Add(24 * time.Hour), wantedCode: TLSValidationNotYetValid},
		{name: "已经过期", notBefore: now.Add(-48 * time.Hour), notAfter: now.Add(-time.Hour), wantedCode: TLSValidationExpired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pki := newTLSTestPKI(t, now, tlsTestLeafOptions{
				notBefore: test.notBefore,
				notAfter:  test.notAfter,
				dnsNames:  []string{"api.example.com", "*.tenant.example.com"},
				ipAddresses: []net.IP{
					net.ParseIP("192.0.2.10"),
				},
			})
			app, configuration := newDeployTestApp(t)
			certPath, keyPath := writeTLSTestMaterial(t, app.BasePath, pki.leafKey, 0o600, pki.leafDER, pki.intermediateDER)
			configureTLSTestApp(configuration, certPath, keyPath)
			_, err := validateTLSMaterial(app, configuration, tlsValidationOptions{now: now, roots: pki.rootPool})
			assertTLSValidationCode(t, err, test.wantedCode)
		})
	}
}

// TestTLSValidationRejectsHostnameChainUsageAndWeakKey 验证 SAN、证书链、服务端用途和算法强度边界。
func TestTLSValidationRejectsHostnameChainUsageAndWeakKey(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name       string
		options    tlsTestLeafOptions
		omitChain  bool
		wantedCode TLSValidationCode
	}{
		{
			name:       "SAN 不覆盖配置域名",
			options:    tlsTestLeafOptions{dnsNames: []string{"other.example.com"}},
			wantedCode: TLSValidationHostname,
		},
		{
			name:       "缺少中间证书",
			options:    tlsTestLeafOptions{dnsNames: []string{"api.example.com", "*.tenant.example.com"}, ipAddresses: []net.IP{net.ParseIP("192.0.2.10")}},
			omitChain:  true,
			wantedCode: TLSValidationChain,
		},
		{
			name:       "缺少服务端用途",
			options:    tlsTestLeafOptions{dnsNames: []string{"api.example.com", "*.tenant.example.com"}, ipAddresses: []net.IP{net.ParseIP("192.0.2.10")}, usages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}},
			wantedCode: TLSValidationUsage,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pki := newTLSTestPKI(t, now, test.options)
			app, configuration := newDeployTestApp(t)
			chain := [][]byte{pki.leafDER, pki.intermediateDER}
			if test.omitChain {
				chain = chain[:1]
			}
			certPath, keyPath := writeTLSTestMaterial(t, app.BasePath, pki.leafKey, 0o600, chain...)
			configureTLSTestApp(configuration, certPath, keyPath)
			_, err := validateTLSMaterial(app, configuration, tlsValidationOptions{now: now, roots: pki.rootPool})
			assertTLSValidationCode(t, err, test.wantedCode)
		})
	}

	weakKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("生成弱 RSA 私钥失败: %v", err)
	}
	pki := newTLSTestPKI(t, now, tlsTestLeafOptions{
		dnsNames:    []string{"api.example.com", "*.tenant.example.com"},
		ipAddresses: []net.IP{net.ParseIP("192.0.2.10")},
		key:         weakKey,
	})
	app, configuration := newDeployTestApp(t)
	certPath, keyPath := writeTLSTestMaterial(t, app.BasePath, pki.leafKey, 0o600, pki.leafDER, pki.intermediateDER)
	configureTLSTestApp(configuration, certPath, keyPath)
	_, validationErr := validateTLSMaterial(app, configuration, tlsValidationOptions{now: now, roots: pki.rootPool})
	assertTLSValidationCode(t, validationErr, TLSValidationAlgorithm)
}

// TestTLSValidationEnforcesPrivateKeyPermissions 验证类 Unix 私钥不能向组或其他用户开放。
func TestTLSValidationEnforcesPrivateKeyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 权限边界由 ACL 警告覆盖")
	}
	now := time.Now().UTC()
	pki := newTLSTestPKI(t, now, tlsTestLeafOptions{dnsNames: []string{"api.example.com", "*.tenant.example.com"}, ipAddresses: []net.IP{net.ParseIP("192.0.2.10")}})
	app, configuration := newDeployTestApp(t)
	certPath, keyPath := writeTLSTestMaterial(t, app.BasePath, pki.leafKey, 0o644, pki.leafDER, pki.intermediateDER)
	configureTLSTestApp(configuration, certPath, keyPath)

	_, err := validateTLSMaterial(app, configuration, tlsValidationOptions{now: now, roots: pki.rootPool})
	assertTLSValidationCode(t, err, TLSValidationPermission)
}

// TestTLSAlgorithmPolicyAcceptsModernKeysAndRejectsLegacySignatures 验证算法门禁的明确兼容矩阵。
func TestTLSAlgorithmPolicyAcceptsModernKeysAndRejectsLegacySignatures(t *testing.T) {
	ed25519Public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成 Ed25519 测试密钥失败: %v", err)
	}
	modern := []*x509.Certificate{
		{SignatureAlgorithm: x509.ECDSAWithSHA256, PublicKey: mustTLSTestECDSAKey(t).Public()},
		{SignatureAlgorithm: x509.PureEd25519, PublicKey: ed25519Public},
	}
	for _, certificate := range modern {
		if err := validateTLSCertificateAlgorithm(certificate); err != nil {
			t.Fatalf("现代 TLS 算法不应被拒绝: key=%T err=%v", certificate.PublicKey, err)
		}
	}
	legacy := []*x509.Certificate{
		{SignatureAlgorithm: x509.SHA1WithRSA, PublicKey: mustTLSTestECDSAKey(t).Public()},
		{SignatureAlgorithm: x509.ECDSAWithSHA256, PublicKey: struct{}{}},
	}
	for _, certificate := range legacy {
		err := validateTLSCertificateAlgorithm(certificate)
		assertTLSValidationCode(t, err, TLSValidationAlgorithm)
	}
}

// TestTLSPrivateKeyParserSupportsStandardUnencryptedFormats 验证服务端兼容 PKCS#1 和 SEC1 标准私钥格式。
func TestTLSPrivateKeyParserSupportsStandardUnencryptedFormats(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, tlsMinimumRSAKeyBits)
	if err != nil {
		t.Fatalf("生成 RSA 测试密钥失败: %v", err)
	}
	ecdsaKey := mustTLSTestECDSAKey(t)
	encodedEC, err := x509.MarshalECPrivateKey(ecdsaKey)
	if err != nil {
		t.Fatalf("编码 SEC1 私钥失败: %v", err)
	}
	formats := [][]byte{
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: encodedEC}),
	}
	for _, encoded := range formats {
		if _, err := parseTLSPrivateKey(encoded); err != nil {
			t.Fatalf("标准未加密私钥格式应被接受: %v", err)
		}
	}
	if _, err := parseTLSPrivateKey(pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte("ciphertext")})); err == nil {
		t.Fatal("无法由 Go TLS 服务直接加载的加密私钥必须被拒绝")
	}
}

// TestTLSTargetNormalizationHandlesPortsAndIPv6 验证证书目标从 Host 白名单剥离端口并保留规范 IP。
func TestTLSTargetNormalizationHandlesPortsAndIPv6(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "API.Example.COM:443", want: "api.example.com"},
		{raw: "[2001:db8::1]:8443", want: "2001:db8::1"},
		{raw: "*.Tenant.Example.com:443", want: "*.tenant.example.com"},
	}
	for _, test := range tests {
		got, err := normalizeTLSTarget(test.raw)
		if err != nil || got != test.want {
			t.Fatalf("TLS 目标规范化错误: raw=%q want=%q got=%q err=%v", test.raw, test.want, got, err)
		}
	}
	for _, raw := range []string{"*", "*.192.0.2.1", "bad host.example.com", "-bad.example.com"} {
		if _, err := normalizeTLSTarget(raw); err == nil {
			t.Fatalf("非法 TLS 目标必须被拒绝: %q", raw)
		}
	}
}

func configureTLSTestApp(configuration *config.Config, certPath, keyPath string) {
	configuration.Set("app.server.tls.enable", true)
	configuration.Set("app.server.tls.cert_file", certPath)
	configuration.Set("app.server.tls.key_file", keyPath)
	configuration.Set("app.server.domain", "https://api.example.com")
	configuration.Set("app.server.allowed_hosts", []interface{}{"api.example.com", "*.tenant.example.com", "192.0.2.10"})
}

func writeTLSTestMaterial(t *testing.T, basePath string, privateKey crypto.Signer, mode os.FileMode, certificates ...[]byte) (string, string) {
	t.Helper()
	certPath := filepath.Join(basePath, "server-cert.pem")
	keyPath := filepath.Join(basePath, "server-key.pem")
	certificatePEM := make([]byte, 0)
	for _, certificate := range certificates {
		certificatePEM = append(certificatePEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate})...)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("编码测试私钥失败: %v", err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	if err := os.WriteFile(certPath, certificatePEM, 0o644); err != nil {
		t.Fatalf("写入测试证书链失败: %v", err)
	}
	if err := os.WriteFile(keyPath, privatePEM, mode); err != nil {
		t.Fatalf("写入测试私钥失败: %v", err)
	}
	if err := os.Chmod(keyPath, mode); err != nil {
		t.Fatalf("设置测试私钥权限失败: %v", err)
	}
	return certPath, keyPath
}

func newTLSTestPKI(t *testing.T, now time.Time, options tlsTestLeafOptions) tlsTestPKI {
	t.Helper()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成测试根密钥失败: %v", err)
	}
	rootTemplate := &x509.Certificate{
		SerialNumber:          tlsTestSerial(t),
		Subject:               pkix.Name{CommonName: "ThinkGo Test Root"},
		NotBefore:             now.Add(-365 * 24 * time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            1,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, rootKey.Public(), rootKey)
	if err != nil {
		t.Fatalf("创建测试根证书失败: %v", err)
	}
	rootCertificate, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatalf("解析测试根证书失败: %v", err)
	}

	intermediateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成测试中间密钥失败: %v", err)
	}
	intermediateTemplate := &x509.Certificate{
		SerialNumber:          tlsTestSerial(t),
		Subject:               pkix.Name{CommonName: "ThinkGo Test Intermediate"},
		NotBefore:             now.Add(-180 * 24 * time.Hour),
		NotAfter:              now.Add(180 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	intermediateDER, err := x509.CreateCertificate(rand.Reader, intermediateTemplate, rootCertificate, intermediateKey.Public(), rootKey)
	if err != nil {
		t.Fatalf("创建测试中间证书失败: %v", err)
	}
	intermediateCertificate, err := x509.ParseCertificate(intermediateDER)
	if err != nil {
		t.Fatalf("解析测试中间证书失败: %v", err)
	}

	leafKey := options.key
	if leafKey == nil {
		leafKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("生成测试叶子密钥失败: %v", err)
		}
	}
	if options.notBefore.IsZero() {
		options.notBefore = now.Add(-time.Hour)
	}
	if options.notAfter.IsZero() {
		options.notAfter = now.Add(30 * 24 * time.Hour)
	}
	if len(options.usages) == 0 {
		options.usages = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	leafTemplate := &x509.Certificate{
		SerialNumber:          tlsTestSerial(t),
		Subject:               pkix.Name{CommonName: "api.example.com"},
		NotBefore:             options.notBefore,
		NotAfter:              options.notAfter,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           options.usages,
		DNSNames:              options.dnsNames,
		IPAddresses:           options.ipAddresses,
	}
	if _, ok := leafKey.(*rsa.PrivateKey); ok {
		leafTemplate.KeyUsage |= x509.KeyUsageKeyEncipherment
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, intermediateCertificate, leafKey.Public(), intermediateKey)
	if err != nil {
		t.Fatalf("创建测试叶子证书失败: %v", err)
	}
	rootPool := x509.NewCertPool()
	rootPool.AddCert(rootCertificate)
	return tlsTestPKI{
		now:             now,
		rootCertificate: rootCertificate,
		rootPool:        rootPool,
		intermediateDER: intermediateDER,
		leafDER:         leafDER,
		leafKey:         leafKey,
	}
}

func tlsTestSerial(t *testing.T) *big.Int {
	t.Helper()
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil || serial.Sign() == 0 {
		t.Fatalf("生成测试证书序列号失败: serial=%v err=%v", serial, err)
	}
	return serial
}

func mustTLSTestECDSAKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成 ECDSA 测试密钥失败: %v", err)
	}
	return privateKey
}

func assertTLSValidationCode(t *testing.T, err error, wanted TLSValidationCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("TLS 验证应失败: want=%s", wanted)
	}
	var validationError *TLSValidationError
	if !errors.As(err, &validationError) {
		t.Fatalf("TLS 验证必须返回类型化错误: %T %v", err, err)
	}
	if validationError.Code != wanted {
		t.Fatalf("TLS 验证错误类型不符: want=%s got=%s err=%v", wanted, validationError.Code, err)
	}
}
