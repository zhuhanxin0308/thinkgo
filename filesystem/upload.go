package filesystem

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"path/filepath"
	"strings"
	"time"
)

// HashNameRule 自定义上传文件名规则，对应 ThinkPHP File::hashName 的 Closure 参数。
type HashNameRule func(file *multipart.FileHeader) (string, error)

// PutFile 按哈希规则生成文件名并保存上传文件。
//
// 可选参数依次为规则和写入配置；规则支持 md5、sha1、sha256、sha384、sha512
// 或 HashNameRule。省略规则时使用日期目录与随机 32 位名称。
func (disk *Local) PutFile(directory string, file *multipart.FileHeader, arguments ...interface{}) (string, error) {
	if file == nil {
		return "", fmt.Errorf("%w: 上传文件不能为空", ErrInvalidConfiguration)
	}
	if len(arguments) > 2 {
		return "", fmt.Errorf("%w: putFile 最多接收规则和配置两个可选参数", ErrInvalidConfiguration)
	}
	var rule interface{}
	options := map[string]interface{}{}
	if len(arguments) > 0 {
		rule = arguments[0]
	}
	if len(arguments) == 2 {
		configuredOptions, ok := arguments[1].(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("%w: putFile 配置必须是对象", ErrInvalidConfiguration)
		}
		options = configuredOptions
	}
	name, err := uploadHashName(file, rule)
	if err != nil {
		return "", err
	}
	return disk.PutFileAs(directory, file, name, options)
}

// PutFileAs 使用指定文件名保存上传文件，并返回磁盘内的相对路径。
func (disk *Local) PutFileAs(directory string, file *multipart.FileHeader, name string, optionSets ...map[string]interface{}) (string, error) {
	if file == nil {
		return "", fmt.Errorf("%w: 上传文件不能为空", ErrInvalidConfiguration)
	}
	options, err := singleOptions(optionSets)
	if err != nil {
		return "", err
	}
	storedPath, err := normalizeFilePath(strings.Trim(directory+"/"+name, "/"))
	if err != nil {
		return "", err
	}
	opened, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("打开上传文件失败: %w", err)
	}
	if err = disk.writeReaderWithSource(
		storedPath,
		opened,
		opened,
		"关闭上传文件失败",
		[]map[string]interface{}{options},
	); err != nil {
		return "", err
	}
	return storedPath, nil
}

func uploadHashName(file *multipart.FileHeader, rule interface{}) (string, error) {
	baseName := ""
	switch current := rule.(type) {
	case nil:
		randomBytes := make([]byte, 16)
		if _, err := rand.Read(randomBytes); err != nil {
			return "", fmt.Errorf("生成上传文件名失败: %w", err)
		}
		baseName = time.Now().Format("20060102") + "/" + hex.EncodeToString(randomBytes)
	case string:
		algorithm := strings.ToLower(strings.TrimSpace(current))
		if algorithm == "" {
			return uploadHashName(file, nil)
		}
		digest, err := checksumHash(algorithm)
		if err != nil {
			return "", err
		}
		opened, err := file.Open()
		if err != nil {
			return "", fmt.Errorf("打开上传文件计算哈希失败: %w", err)
		}
		_, hashErr := io.Copy(digest, opened)
		closeErr := opened.Close()
		if hashErr != nil || closeErr != nil {
			return "", errors.Join(
				wrapOperationError("计算上传文件哈希失败", hashErr),
				wrapOperationError("关闭上传哈希文件失败", closeErr),
			)
		}
		hashValue := hex.EncodeToString(digest.Sum(nil))
		baseName = hashValue[:2] + "/" + hashValue[2:]
	case HashNameRule:
		generated, err := current(file)
		if err != nil {
			return "", fmt.Errorf("执行上传文件名规则失败: %w", err)
		}
		baseName = generated
	case func(*multipart.FileHeader) (string, error):
		generated, err := current(file)
		if err != nil {
			return "", fmt.Errorf("执行上传文件名规则失败: %w", err)
		}
		baseName = generated
	case func(*multipart.FileHeader) string:
		baseName = current(file)
	default:
		return "", fmt.Errorf("%w: putFile 文件名规则类型不受支持", ErrInvalidConfiguration)
	}
	baseName = strings.Trim(strings.ReplaceAll(baseName, "\\", "/"), "/")
	if baseName == "" {
		return "", fmt.Errorf("%w: 上传文件名规则返回空名称", ErrInvalidConfiguration)
	}
	extension := strings.TrimPrefix(filepath.Ext(file.Filename), ".")
	if extension != "" {
		baseName += "." + extension
	}
	return baseName, nil
}
