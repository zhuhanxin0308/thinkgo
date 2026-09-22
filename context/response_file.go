package context

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	maxSafeDownloadPathBytes = 4096
	maxDownloadFilenameRunes = 180
)

var (
	// ErrUnsafeDownloadPath 表示安全下载路径逃逸了允许根目录或包含危险路径语法。
	ErrUnsafeDownloadPath = errors.New("安全下载路径越界")
	// ErrDownloadNotRegular 表示下载目标不是普通文件。
	ErrDownloadNotRegular = errors.New("下载目标不是普通文件")
)

// Download 设置受信任路径的文件下载响应；外部输入路径必须改用 DownloadSafe。
func (r *Response) Download(filePath, filename string) *Response {
	if r == nil {
		return nil
	}
	r.clearEntitySource()
	safeName := sanitizeDownloadFilename(filename)
	fallbackName := asciiDownloadFilename(safeName)
	r.Header("Content-Type", "application/octet-stream")
	r.Header("X-Content-Type-Options", "nosniff")
	r.Header(
		"Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, fallbackName, url.PathEscape(safeName)),
	)
	r.Header("Expires", "0")
	// 下载默认可能包含私有数据，禁止共享缓存和浏览器持久缓存。
	r.Header("Cache-Control", "private, no-store")
	r.Header("Pragma", "no-cache")
	r.filePath = filePath
	return r
}

// DownloadSafe 把可能来自外部的相对路径限制在指定根目录，并在发送阶段再次验证真实路径。
func (r *Response) DownloadSafe(baseDir, relativePath, filename string) *Response {
	if r == nil {
		return nil
	}
	target, ok := resolveWithinBase(baseDir, relativePath)
	if !ok {
		r.addError(fmt.Errorf("%w: %q", ErrUnsafeDownloadPath, relativePath))
		return r.Abort(http.StatusForbidden, map[string]interface{}{"message": "invalid download path"})
	}
	baseAbsolute, err := filepath.Abs(baseDir)
	if err != nil {
		r.addError(fmt.Errorf("%w: %v", ErrUnsafeDownloadPath, err))
		return r.Abort(http.StatusForbidden, map[string]interface{}{"message": "invalid download path"})
	}
	if strings.TrimSpace(filename) == "" {
		filename = filepath.Base(target)
	}
	r.Download(target, filename)
	r.fileRoot = filepath.Clean(baseAbsolute)
	return r
}

// sendFile 在提交响应头前打开并复核文件，避免失败响应残留下载头或跟随越界符号链接。
func (r *Response) sendFile(w http.ResponseWriter) error {
	file, info, err := r.openDownloadFile()
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, ErrUnsafeDownloadPath):
			status = http.StatusForbidden
		case errors.Is(err, os.ErrNotExist), errors.Is(err, ErrDownloadNotRegular):
			status = http.StatusNotFound
		}
		writeErr := writePlainHTTPError(w, status)
		return errors.Join(r.err, err, markResponseTransmissionError(writeErr))
	}

	r.writeHeaders(w)
	if err := r.saveSession(w); err != nil {
		_ = file.Close()
		return errors.Join(r.err, err)
	}
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(r.status)
	_, copyErr := io.Copy(w, file)
	closeErr := file.Close()
	return errors.Join(r.err, markResponseTransmissionError(copyErr), closeErr)
}

func (r *Response) openDownloadFile() (*os.File, os.FileInfo, error) {
	if r.fileRoot == "" {
		file, err := os.Open(r.filePath)
		if err != nil {
			return nil, nil, err
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			if err != nil {
				return nil, nil, err
			}
			return nil, nil, ErrDownloadNotRegular
		}
		return file, info, nil
	}

	rootReal, err := filepath.EvalSymlinks(r.fileRoot)
	if err != nil {
		return nil, nil, err
	}
	targetReal, err := filepath.EvalSymlinks(r.filePath)
	if err != nil {
		return nil, nil, err
	}
	if !pathWithinDownloadRoot(rootReal, targetReal) {
		return nil, nil, ErrUnsafeDownloadPath
	}

	file, err := os.Open(targetReal)
	if err != nil {
		return nil, nil, err
	}
	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return nil, nil, statErr
	}
	verifiedReal, verifyErr := filepath.EvalSymlinks(r.filePath)
	if verifyErr != nil {
		_ = file.Close()
		return nil, nil, verifyErr
	}
	if !pathWithinDownloadRoot(rootReal, verifiedReal) {
		_ = file.Close()
		return nil, nil, ErrUnsafeDownloadPath
	}
	verifiedInfo, verifiedStatErr := os.Stat(verifiedReal)
	if verifiedStatErr != nil {
		_ = file.Close()
		return nil, nil, verifiedStatErr
	}
	if !os.SameFile(info, verifiedInfo) || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, ErrDownloadNotRegular
	}
	return file, info, nil
}

// resolveWithinBase 完成跨平台词法校验，并对已经存在的目标提前验证真实路径归属。
func resolveWithinBase(baseDir, relativePath string) (string, bool) {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" || relativePath == "" || len(relativePath) > maxSafeDownloadPathBytes || strings.ContainsAny(relativePath, "\x00\r\n:") {
		return "", false
	}
	normalized := strings.ReplaceAll(relativePath, "\\", "/")
	if path.IsAbs(normalized) || filepath.IsAbs(relativePath) || filepath.VolumeName(relativePath) != "" {
		return "", false
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}

	baseAbsolute, err := filepath.Abs(baseDir)
	if err != nil {
		return "", false
	}
	targetAbsolute, err := filepath.Abs(filepath.Join(baseAbsolute, filepath.FromSlash(cleaned)))
	if err != nil || !pathWithinDownloadRoot(baseAbsolute, targetAbsolute) {
		return "", false
	}
	baseReal, baseErr := filepath.EvalSymlinks(baseAbsolute)
	targetReal, targetErr := filepath.EvalSymlinks(targetAbsolute)
	if baseErr == nil && targetErr == nil && !pathWithinDownloadRoot(baseReal, targetReal) {
		return "", false
	}
	return targetAbsolute, true
}

func pathWithinDownloadRoot(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || filepath.IsAbs(relative) {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func sanitizeDownloadFilename(name string) string {
	if index := strings.LastIndexAny(name, `/\`); index >= 0 {
		name = name[index+1:]
	}
	name = strings.Map(func(char rune) rune {
		if char < 0x20 || char == 0x7f || char == '"' || char == '\\' {
			return -1
		}
		return char
	}, name)
	runes := []rune(strings.TrimSpace(name))
	if len(runes) > maxDownloadFilenameRunes {
		runes = runes[:maxDownloadFilenameRunes]
	}
	name = string(runes)
	if name == "" || name == "." || name == ".." {
		return "download"
	}
	return name
}

func asciiDownloadFilename(name string) string {
	result := strings.Map(func(char rune) rune {
		if char < 0x20 || char > 0x7e || char == '"' || char == '\\' {
			return '_'
		}
		return char
	}, name)
	if strings.TrimSpace(result) == "" {
		return "download"
	}
	return result
}

func writePlainHTTPError(w http.ResponseWriter, status int) error {
	if isNilHTTPResponseWriter(w) {
		return ErrInvalidResponseWriter
	}
	header := w.Header()
	for key := range header {
		header.Del(key)
	}
	header.Set("Content-Type", "text/plain; charset=utf-8")
	header.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	return writeCompleteResponseBody(w, []byte(http.StatusText(status)))
}
