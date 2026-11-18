package filesystem

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"strings"
	"time"
)

// HashNameRule 自定义上传文件名规则，对应 ThinkPHP File::hashName 的 Closure 参数。
type HashNameRule func(file *multipart.FileHeader) (string, error)

// PutFile 按哈希规则生成文件名并保存上传文件。
//
// 可选参数依次为规则和写入配置；规则支持 md5、sha1、sha256、sha384、sha512
// 或 HashNameRule。省略规则时使用日期目录与随机 32 位名称。
// 配置 upload_policy 可显式传入 *UploadPolicy，执行大小、扩展名和内容类型校验。
// 启用策略时只支持内置命名规则；未配置策略默认拒绝。
// 已经由业务校验的可信输入可显式设置 trusted_upload: true。
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
	policy, err := uploadPolicyOption(options)
	if err != nil {
		return "", err
	}
	if policy != nil {
		return disk.putFileWithPolicy(directory, file, "", rule, true, options, policy)
	}
	name, err := uploadHashName(file, rule)
	if err != nil {
		return "", err
	}
	return disk.putTrustedUpload(directory, file, name, false, options)
}

// PutFileAs 使用指定文件名保存上传文件，并返回磁盘内的相对路径。
// 可通过 upload_policy 显式选择 *UploadPolicy；策略同时校验源和最终扩展名。
// 无策略默认拒绝；可信应用输入必须显式设置 trusted_upload: true。
func (disk *Local) PutFileAs(directory string, file *multipart.FileHeader, name string, optionSets ...map[string]interface{}) (string, error) {
	if file == nil {
		return "", fmt.Errorf("%w: 上传文件不能为空", ErrInvalidConfiguration)
	}
	options, err := singleOptions(optionSets)
	if err != nil {
		return "", err
	}
	policy, err := uploadPolicyOption(options)
	if err != nil {
		return "", err
	}
	if policy != nil {
		return disk.putFileWithPolicy(directory, file, name, nil, false, options, policy)
	}
	return disk.putTrustedUpload(directory, file, name, true, options)
}

func (disk *Local) putTrustedUpload(directory string, file *multipart.FileHeader, name string, singleSegment bool, options map[string]interface{}) (string, error) {
	storedPath, err := resolveUploadDestination(directory, name, singleSegment, false)
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

func resolveUploadDestination(directory, name string, singleSegment, policyError bool) (string, error) {
	normalizedDir, err := normalizePath(directory)
	if err != nil {
		return "", err
	}
	slashedName := strings.ReplaceAll(name, "\\", "/")
	for _, part := range strings.Split(slashedName, "/") {
		if part == ".." {
			return "", fmt.Errorf("%w: 上传文件名不能包含父目录段", ErrInvalidPath)
		}
	}
	if singleSegment && strings.Contains(slashedName, "/") {
		sepErr := fmt.Errorf("%w: 上传文件名不能包含路径分隔符", ErrInvalidPath)
		if policyError {
			return "", errors.Join(ErrUploadTypeNotAllowed, sepErr)
		}
		return "", sepErr
	}
	normalizedName, err := normalizeFilePath(name)
	if err != nil {
		if policyError && errors.Is(err, errWindowsPathAlias) {
			return "", errors.Join(ErrUploadTypeNotAllowed, err)
		}
		return "", err
	}
	if normalizedDir == "" {
		return normalizedName, nil
	}
	key := normalizedDir + "/" + normalizedName
	if !strings.HasPrefix(key, normalizedDir+"/") {
		return "", fmt.Errorf("%w: 上传目标超出指定目录", ErrInvalidPath)
	}
	return key, nil
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
	extension := uploadExtension(file.Filename)
	if extension != "" {
		baseName += "." + extension
	}
	return baseName, nil
}
