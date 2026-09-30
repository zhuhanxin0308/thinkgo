package util

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

// 证书编码失败不得返回部分证书或已生成的私钥，也不修改全局随机源来制造错误。
func TestDevelopmentCertificateRejectsUnencodableValidity(t *testing.T) {
	for _, test := range []struct {
		name string
		now  time.Time
	}{
		{"before ASN.1 range", time.Date(-1, time.June, 1, 12, 0, 0, 0, time.UTC)},
		{"expiry beyond ASN.1 range", time.Date(9999, time.June, 1, 12, 0, 0, 0, time.UTC)},
	} {
		t.Run(test.name, func(t *testing.T) {
			certificate, key, err := generateDevelopmentCertificate(test.now)
			if err == nil {
				t.Fatal("unencodable validity must fail certificate generation")
			}
			if certificate != nil || key != nil {
				t.Fatal("failed generation must not expose a partial certificate or private key")
			}
		})
	}
}

// 跨越 UTCTime/GeneralizedTime 编码边界时仍保持真实的有效期和公私钥匹配。
func TestDevelopmentCertificateAcrossTimeEncodingBoundary(t *testing.T) {
	now := time.Date(2049, time.December, 31, 23, 59, 0, 0, time.UTC)
	certificatePEM, keyPEM, err := generateDevelopmentCertificate(now)
	if err != nil {
		t.Fatal(err)
	}
	certificateBlock, rest := pem.Decode(certificatePEM)
	if certificateBlock == nil || certificateBlock.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatal("invalid certificate PEM")
	}
	certificate, err := x509.ParseCertificate(certificateBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, rest := pem.Decode(keyPEM)
	if keyBlock == nil || keyBlock.Type != "EC PRIVATE KEY" || len(rest) != 0 {
		t.Fatal("invalid private key PEM")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, ok := certificate.PublicKey.(*ecdsa.PublicKey)
	if !ok || !publicKey.Equal(key.Public()) {
		t.Fatal("certificate and private key must match")
	}
	if !certificate.NotBefore.Equal(now.Add(-developmentCertificateClockSkew)) || !certificate.NotAfter.Equal(now.AddDate(1, 0, 0)) {
		t.Fatalf("validity changed across ASN.1 time encodings: %s to %s", certificate.NotBefore, certificate.NotAfter)
	}
	if err := certificate.CheckSignature(certificate.SignatureAlgorithm, certificate.RawTBSCertificate, certificate.Signature); err != nil {
		t.Fatal(err)
	}
}
