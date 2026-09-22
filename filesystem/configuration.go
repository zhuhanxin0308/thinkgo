package filesystem

import (
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

const (
	// VisibilityPublic 对应 Flysystem Visibility::PUBLIC。
	VisibilityPublic = "public"
	// VisibilityPrivate 对应 Flysystem Visibility::PRIVATE。
	VisibilityPrivate = "private"
)

const (
	defaultFilePublicMode       fs.FileMode = 0o644
	defaultFilePrivateMode      fs.FileMode = 0o600
	defaultDirectoryPublicMode  fs.FileMode = 0o755
	defaultDirectoryPrivateMode fs.FileMode = 0o700
)

type linkHandling uint8

const (
	disallowLinks linkHandling = iota
	skipLinks
)

// Permissions 保存 public/private 文件与目录权限，默认值与
// PortableVisibilityConverter 完全一致。
type Permissions struct {
	FilePublic       fs.FileMode
	FilePrivate      fs.FileMode
	DirectoryPublic  fs.FileMode
	DirectoryPrivate fs.FileMode
}

// LocalConfig 是 ThinkPHP Local 驱动配置在 Go 中的结构化表示。
type LocalConfig struct {
	Root        string
	URL         string
	Visibility  string
	Permissions Permissions
	Links       string
}

func parseLocalConfig(configuration map[string]interface{}) (LocalConfig, error) {
	root, ok := configuration["root"].(string)
	if !ok || strings.TrimSpace(root) == "" {
		return LocalConfig{}, fmt.Errorf("%w: local.root 必须是非空字符串", ErrInvalidConfiguration)
	}
	result := LocalConfig{Root: root}
	if raw, exists := configuration["url"]; exists {
		value, valid := raw.(string)
		if !valid {
			return LocalConfig{}, fmt.Errorf("%w: local.url 必须是字符串", ErrInvalidConfiguration)
		}
		result.URL = value
	}
	if raw, exists := configuration["visibility"]; exists {
		value, valid := raw.(string)
		if !valid {
			return LocalConfig{}, fmt.Errorf("%w: local.visibility 必须是字符串", ErrInvalidConfiguration)
		}
		result.Visibility = value
	}
	if raw, exists := configuration["links"]; exists {
		value, valid := raw.(string)
		if !valid {
			return LocalConfig{}, fmt.Errorf("%w: local.links 必须是字符串", ErrInvalidConfiguration)
		}
		result.Links = value
	}
	if raw, exists := configuration["permissions"]; exists {
		permissions, valid := raw.(map[string]interface{})
		if !valid {
			return LocalConfig{}, fmt.Errorf("%w: local.permissions 必须是对象", ErrInvalidConfiguration)
		}
		parsed, err := parsePermissions(permissions)
		if err != nil {
			return LocalConfig{}, err
		}
		result.Permissions = parsed
	}
	return result, nil
}

func normalizeLocalConfig(configuration LocalConfig) (LocalConfig, error) {
	if strings.TrimSpace(configuration.Root) == "" {
		return LocalConfig{}, fmt.Errorf("%w: local.root 必须是非空字符串", ErrInvalidConfiguration)
	}
	if configuration.Visibility == "" {
		configuration.Visibility = VisibilityPrivate
	}
	if err := validateVisibility(configuration.Visibility); err != nil {
		return LocalConfig{}, err
	}
	configuration.Permissions = withDefaultPermissions(configuration.Permissions)
	return configuration, nil
}

func withDefaultPermissions(permissions Permissions) Permissions {
	if permissions.FilePublic == 0 {
		permissions.FilePublic = defaultFilePublicMode
	}
	if permissions.FilePrivate == 0 {
		permissions.FilePrivate = defaultFilePrivateMode
	}
	if permissions.DirectoryPublic == 0 {
		permissions.DirectoryPublic = defaultDirectoryPublicMode
	}
	if permissions.DirectoryPrivate == 0 {
		permissions.DirectoryPrivate = defaultDirectoryPrivateMode
	}
	return permissions
}

func parsePermissions(configuration map[string]interface{}) (Permissions, error) {
	permissions := Permissions{}
	for groupName, destination := range map[string]struct {
		public  *fs.FileMode
		private *fs.FileMode
	}{
		"file": {public: &permissions.FilePublic, private: &permissions.FilePrivate},
		"dir":  {public: &permissions.DirectoryPublic, private: &permissions.DirectoryPrivate},
	} {
		rawGroup, exists := configuration[groupName]
		if !exists {
			continue
		}
		group, ok := rawGroup.(map[string]interface{})
		if !ok {
			return Permissions{}, fmt.Errorf("%w: permissions.%s 必须是对象", ErrInvalidConfiguration, groupName)
		}
		for visibility, target := range map[string]*fs.FileMode{
			VisibilityPublic: destination.public, VisibilityPrivate: destination.private,
		} {
			rawMode, configured := group[visibility]
			if !configured {
				continue
			}
			mode, err := parseFileMode(rawMode)
			if err != nil {
				return Permissions{}, fmt.Errorf("%w: permissions.%s.%s: %v", ErrInvalidConfiguration, groupName, visibility, err)
			}
			*target = mode
		}
	}
	return withDefaultPermissions(permissions), nil
}

func parseFileMode(value interface{}) (fs.FileMode, error) {
	var parsed uint64
	var err error
	switch current := value.(type) {
	case int:
		if current < 0 {
			return 0, fmt.Errorf("权限不能为负数")
		}
		parsed = uint64(current)
	case int64:
		if current < 0 {
			return 0, fmt.Errorf("权限不能为负数")
		}
		parsed = uint64(current)
	case float64:
		if current < 0 || current != float64(uint64(current)) {
			return 0, fmt.Errorf("权限必须是整数")
		}
		parsed = uint64(current)
	case string:
		text := strings.TrimSpace(current)
		base := 10
		if strings.HasPrefix(text, "0") {
			base = 8
		}
		parsed, err = strconv.ParseUint(text, base, 16)
		if err != nil {
			return 0, fmt.Errorf("权限字符串无效")
		}
	default:
		return 0, fmt.Errorf("权限必须是整数或八进制字符串")
	}
	if parsed == 0 || parsed > 0o777 {
		return 0, fmt.Errorf("权限必须位于 0001 到 0777")
	}
	return fs.FileMode(parsed), nil
}

func validateVisibility(visibility string) error {
	if visibility != VisibilityPublic && visibility != VisibilityPrivate {
		return fmt.Errorf("%w: visibility 必须是 public 或 private", ErrInvalidConfiguration)
	}
	return nil
}

func (configuration LocalConfig) linkHandling() linkHandling {
	if configuration.Links == "skip" {
		return skipLinks
	}
	return disallowLinks
}

func (configuration LocalConfig) fileMode(visibility string) fs.FileMode {
	if visibility == VisibilityPublic {
		return configuration.Permissions.FilePublic
	}
	return configuration.Permissions.FilePrivate
}

func (configuration LocalConfig) directoryMode(visibility string) fs.FileMode {
	if visibility == VisibilityPublic {
		return configuration.Permissions.DirectoryPublic
	}
	return configuration.Permissions.DirectoryPrivate
}
