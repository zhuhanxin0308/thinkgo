package command

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/console"
	"github.com/zhuhanxin0308/thinkgo/v3/deploy"
)

func TestDeployCheckSelectedApplicationRetainsProjectTLSListener(t *testing.T) {
	project := newCommandMultiApplicationProject(t)
	if err := project.ApplyProjectApplicationOverrides(map[string]interface{}{
		"database_startup_policy": "disabled",
		"server.tls.enable":       true,
		"server.tls.cert_file":    "host-certificate.pem",
		"server.tls.key_file":     "host-missing.key",
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project.BasePath, "host-certificate.pem"), []byte("listener-material-marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	childConfigPath := filepath.Join(project.BasePath, "app", "admin", "config")
	if err := os.MkdirAll(childConfigPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childConfigPath, "app.json"), []byte(`{"server":{"tls":{"enable":false,"cert_file":"unused-child.pem","key_file":"unused-child.key"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout := &bytes.Buffer{}
	command := &DeployCheck{Command: console.Command{App: project}}
	err := command.Execute(parseMigrationCommandInput(t, command, "--app", "admin"), console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false))
	if !errors.Is(err, deploy.ErrDeploymentBlocked) {
		t.Fatalf("missing host private key must block the selected application's audit: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "Application: admin") || strings.Contains(output, "Application: index") {
		t.Fatalf("--app did not restrict the application audit: %q", output)
	}
	if !strings.Contains(output, "TLS 私钥文件不可用") || strings.Contains(output, "生产部署未启用 TLS") {
		t.Fatalf("--app changed the listener TLS source: %q", output)
	}
	applications, err := project.BuildApplications()
	if err != nil {
		t.Fatal(err)
	}
	if applications["admin"].Config().GetBool("app.server.tls.enable") {
		t.Fatal("fixture must keep the selected application's TLS override separate")
	}
}
