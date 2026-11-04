// Package httplistener shares listener configuration selection and TLS parsing
// between the HTTP host and deployment diagnostics.
package httplistener

import (
	"fmt"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/config"
	"github.com/zhuhanxin0308/thinkgo/v3/internal/apppath"
)

// TLSConfig describes the material used by the process listener.
type TLSConfig struct {
	Enable   bool
	CertFile string
	KeyFile  string
}

// ApplicationConfig preserves the HTTP host's explicit source choice. Native
// hosts use the project snapshot; application handlers use their own service.
func ApplicationConfig(app *framework.App, project bool) (interface{}, error) {
	if app == nil {
		return nil, framework.ErrNilApplication
	}
	if project {
		return app.ProjectApplicationConfig(), nil
	}
	configuration, err := framework.ResolveServiceAs[*config.Config](app, framework.ServiceConfig)
	if err != nil {
		return nil, fmt.Errorf("应用配置服务不可用: %w", err)
	}
	return configuration.Get("app", map[string]interface{}{}), nil
}

// HostApplicationConfig selects the actual process listener, including native
// projects with one application and children that share project configuration
// but intentionally do not own the entry application's catalog.
func HostApplicationConfig(app *framework.App) (interface{}, error) {
	return ApplicationConfig(app, len(app.ApplicationNames()) > 0 || app.CurrentApplicationName() != "")
}

// ApplicationTLS parses and resolves TLS material from an app configuration.
func ApplicationTLS(raw interface{}, basePath string) (TLSConfig, error) {
	application, err := object(raw, "app")
	if err != nil {
		return TLSConfig{}, err
	}
	server, err := object(application["server"], "app.server")
	if err != nil {
		return TLSConfig{}, err
	}
	configuration, err := ParseTLS(server["tls"])
	if err != nil {
		return TLSConfig{}, err
	}
	return configuration.AtPath(basePath), nil
}

// ParseTLS applies the listener's defaults and strict field types. It does not
// read certificate material or relax the host's remaining HTTP validation.
func ParseTLS(raw interface{}) (TLSConfig, error) {
	values, err := object(raw, "app.server.tls")
	if err != nil {
		return TLSConfig{}, err
	}
	for key := range values {
		if key != "enable" && key != "cert_file" && key != "key_file" {
			return TLSConfig{}, fmt.Errorf("app.server.tls 包含未知配置键 %q", key)
		}
	}
	configuration := TLSConfig{CertFile: "./runtime/cert.pem", KeyFile: "./runtime/key.pem"}
	if rawEnable, exists := values["enable"]; exists {
		enabled, ok := rawEnable.(bool)
		if !ok {
			return TLSConfig{}, fmt.Errorf("配置 enable 必须是布尔值，实际类型为 %T", rawEnable)
		}
		configuration.Enable = enabled
	}
	for _, field := range []struct {
		name   string
		target *string
	}{
		{name: "cert_file", target: &configuration.CertFile},
		{name: "key_file", target: &configuration.KeyFile},
	} {
		if value, exists := values[field.name]; exists {
			text, ok := value.(string)
			if !ok {
				return TLSConfig{}, fmt.Errorf("配置 %s 必须是字符串，实际类型为 %T", field.name, value)
			}
			*field.target = strings.TrimSpace(text)
		}
	}
	if configuration.Enable && (configuration.CertFile == "" || configuration.KeyFile == "") {
		return TLSConfig{}, fmt.Errorf("启用 TLS 时证书和私钥路径不能为空")
	}
	return configuration, nil
}

// AtPath anchors relative material paths to the project rather than the
// launcher's working directory, preserving configured absolute paths.
func (configuration TLSConfig) AtPath(basePath string) TLSConfig {
	configuration.CertFile = apppath.Resolve(basePath, configuration.CertFile)
	configuration.KeyFile = apppath.Resolve(basePath, configuration.KeyFile)
	return configuration
}

func object(value interface{}, path string) (map[string]interface{}, error) {
	if value == nil {
		return map[string]interface{}{}, nil
	}
	values, ok := value.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%s 必须是对象，实际类型为 %T", path, value)
	}
	return values, nil
}
