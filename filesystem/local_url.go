package filesystem

import (
	"fmt"
	"net/url"
	"strings"
)

// localFileURL 接受原始文件名；只编码新附加的路径，不重复编码已配置的 URL。
func localFileURL(prefix, logicalPath string) (string, error) {
	base, err := url.Parse(prefix)
	if err != nil || base.Opaque != "" {
		return "", fmt.Errorf("%w: local.url 必须是有效的分层 URL 前缀", ErrInvalidConfiguration)
	}
	// Path 和 RawPath 同步扩展，既保留配置前缀中的有效转义，也不把文件名中的
	// #、?、% 当成 URL 语法。空路径继续返回以 / 结尾的磁盘根地址。
	escapedPrefix := strings.TrimRight(base.EscapedPath(), "/")
	parts := strings.Split(logicalPath, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	base.RawPath = escapedPrefix + "/" + strings.Join(parts, "/")
	base.Path, err = url.PathUnescape(base.RawPath)
	if err != nil {
		return "", fmt.Errorf("%w: local.url 路径编码无效", ErrInvalidConfiguration)
	}
	return base.String(), nil
}
