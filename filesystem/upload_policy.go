package filesystem

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"strings"
)

// ErrUploadRejected identifies a file rejected by an explicit UploadPolicy.
var ErrUploadRejected = errors.New("upload rejected by policy")

// ErrUploadTooLarge means the actual stream exceeded the configured byte limit.
var ErrUploadTooLarge = fmt.Errorf("%w: file exceeds byte limit", ErrUploadRejected)

// ErrUploadTypeNotAllowed means a filename or its detected type was not allowed.
var ErrUploadTypeNotAllowed = fmt.Errorf("%w: filename or content type not allowed", ErrUploadRejected)

// UploadPolicyConfig defines a single-file byte limit and extension-to-MIME pairs.
// Extensions are ASCII alphanumeric suffixes, optionally prefixed with one dot.
// MIME entries are exact media types without parameters or wildcards.
// This is trusted application configuration, not request data.
type UploadPolicyConfig struct {
	MaxBytes     int64
	AllowedTypes map[string][]string
}

// UploadPolicy is an immutable, reusable policy created by NewUploadPolicy.
// Its zero value is invalid. Destination basenames must have a single extension
// and an ASCII alphanumeric, underscore or hyphen stem (at most 255 bytes total).
// Pass it as the "upload_policy" write option to PutFile or PutFileAs. Calls
// without this option retain the trusted-storage behavior. MIME sniffing checks
// only the prefix: this is not a malware scanner or a full format parser.
type UploadPolicy struct {
	maxBytes int64
	allowed  map[string]map[string]struct{}
}

// NewUploadPolicy validates and snapshots a policy. The caller must not mutate
// config while this constructor runs; later changes do not affect the policy.
func NewUploadPolicy(config UploadPolicyConfig) (*UploadPolicy, error) {
	if config.MaxBytes <= 0 || config.MaxBytes == math.MaxInt64 || len(config.AllowedTypes) == 0 {
		return nil, fmt.Errorf("%w: upload policy requires a positive bounded MaxBytes and allowed types", ErrInvalidConfiguration)
	}
	policy := &UploadPolicy{maxBytes: config.MaxBytes, allowed: make(map[string]map[string]struct{}, len(config.AllowedTypes))}
	for extension, types := range config.AllowedTypes {
		extension = strings.TrimPrefix(extension, ".")
		if !validUploadExtension(extension) || len(types) == 0 {
			return nil, fmt.Errorf("%w: upload policy has an invalid extension or empty MIME list", ErrInvalidConfiguration)
		}
		extension = strings.ToLower(extension)
		if _, exists := policy.allowed[extension]; exists {
			return nil, fmt.Errorf("%w: upload policy contains duplicate normalized extensions", ErrInvalidConfiguration)
		}
		allowed := make(map[string]struct{}, len(types))
		for _, value := range types {
			mediaType, parameters, err := mime.ParseMediaType(value)
			if err != nil || len(parameters) != 0 || !strings.Contains(mediaType, "/") || strings.Contains(mediaType, "*") {
				return nil, fmt.Errorf("%w: upload policy requires exact MIME types without parameters", ErrInvalidConfiguration)
			}
			allowed[mediaType] = struct{}{}
		}
		policy.allowed[extension] = allowed
	}
	return policy, nil
}

func validUploadExtension(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func uploadPolicyOption(options map[string]interface{}) (*UploadPolicy, error) {
	raw, exists := options["upload_policy"]
	if !exists {
		return nil, nil
	}
	policy, ok := raw.(*UploadPolicy)
	if !ok || policy == nil || policy.maxBytes <= 0 || policy.maxBytes == math.MaxInt64 || len(policy.allowed) == 0 {
		return nil, fmt.Errorf("%w: upload_policy must be a policy created by NewUploadPolicy", ErrInvalidConfiguration)
	}
	return policy, nil
}

func uploadExtension(name string) string {
	extension := strings.TrimPrefix(path.Ext(strings.ReplaceAll(name, "\\", "/")), ".")
	if !validUploadExtension(extension) {
		return ""
	}
	return strings.ToLower(extension)
}

// Strict destination names use one extension and a restricted application-chosen
// basename. Directory containment is still enforced by normalizeFilePath/os.Root.
func policyDestination(name string) bool {
	base := path.Base(name)
	stem, extension, found := strings.Cut(base, ".")
	if !found || stem == "" || len(base) > 255 || !validUploadExtension(extension) {
		return false
	}
	for _, char := range stem {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func (disk *Local) putFileWithPolicy(directory string, file *multipart.FileHeader, name string, rule interface{}, generated bool, options map[string]interface{}, policy *UploadPolicy) (key string, resultErr error) {
	if disk == nil {
		return "", ErrInvalidConfiguration
	}
	disk.mu.RLock()
	openErr := disk.ensureOpenLocked()
	disk.mu.RUnlock()
	if openErr != nil {
		return "", openErr
	}
	// Custom callbacks can read arbitrary input independently of this policy.
	// Strict uploads instead accept a built-in rule or an explicit PutFileAs name.
	algorithm := ""
	if generated && rule != nil {
		value, ok := rule.(string)
		if !ok {
			return "", fmt.Errorf("%w: upload policy requires a built-in naming rule or PutFileAs", ErrInvalidConfiguration)
		}
		algorithm = strings.ToLower(strings.TrimSpace(value))
		if algorithm != "" {
			if _, err := checksumHash(algorithm); err != nil {
				return "", err
			}
		}
	}
	options = maps.Clone(options)
	delete(options, "upload_policy")
	if _, _, err := disk.writeVisibility(options); err != nil {
		return "", err
	}
	if _, _, err := optionVisibility(options, "directory_visibility", disk.configuration.Visibility, false); err != nil {
		return "", err
	}
	originalExtension := uploadExtension(file.Filename)
	if _, allowed := policy.allowed[originalExtension]; !allowed {
		return "", ErrUploadTypeNotAllowed
	}
	if generated && algorithm == "" {
		var err error
		name, err = uploadHashName(&multipart.FileHeader{Filename: "file." + originalExtension}, nil)
		if err != nil {
			return "", err
		}
	}
	// The digest name is not available until staging. Check other paths first.
	if !generated || algorithm == "" {
		var err error
		key, err = normalizeFilePath(strings.Trim(directory+"/"+name, "/"))
		if err != nil {
			return "", err
		}
		if !policyDestination(key) || policy.allowed[uploadExtension(key)] == nil {
			return "", ErrUploadTypeNotAllowed
		}
	} else if _, err := normalizeFilePath(strings.Trim(directory+"/pending."+originalExtension, "/")); err != nil {
		return "", err
	}

	opened, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("open upload: %w", err)
	}
	staged, stageErr := stagePolicyUpload(opened, policy.maxBytes)
	closeErr := disk.closeOwnedSource(opened)
	if staged != nil {
		defer func() {
			resultErr = errors.Join(resultErr, staged.Close())
			if resultErr != nil {
				key = ""
			}
		}()
	}
	if stageErr != nil || closeErr != nil {
		return "", errors.Join(stageErr, wrapOperationError("close upload", closeErr))
	}
	var prefix [512]byte
	n, err := staged.file.ReadAt(prefix[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("inspect upload: %w", err)
	}
	mediaType, _, err := mime.ParseMediaType(http.DetectContentType(prefix[:n]))
	if err != nil {
		return "", ErrUploadTypeNotAllowed
	}
	if _, allowed := policy.allowed[originalExtension][mediaType]; !allowed {
		return "", ErrUploadTypeNotAllowed
	}
	if generated && algorithm != "" {
		digest, err := checksumHash(algorithm)
		if err != nil {
			return "", err
		}
		if _, err = io.Copy(digest, staged.file); err != nil {
			return "", fmt.Errorf("hash upload snapshot: %w", err)
		}
		value := hex.EncodeToString(digest.Sum(nil))
		name = value[:2] + "/" + value[2:] + "." + originalExtension
		key, err = normalizeFilePath(strings.Trim(directory+"/"+name, "/"))
		if err != nil {
			return "", err
		}
	}
	if _, allowed := policy.allowed[uploadExtension(key)][mediaType]; !allowed {
		return "", ErrUploadTypeNotAllowed
	}
	if _, err = staged.file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind upload snapshot: %w", err)
	}
	if err = disk.writeReaderWithSource(key, staged.file, staged, "close upload snapshot", []map[string]interface{}{options}); err != nil {
		return "", err
	}
	return key, nil
}

// policyUpload owns a private temporary snapshot, closed and removed before the
// existing atomic writer publishes its destination. Close is idempotent.
type policyUpload struct {
	file    *os.File
	closed  bool
	removed bool
}

func (upload *policyUpload) Close() error {
	var closeErr, removeErr error
	if !upload.closed {
		upload.closed = true
		closeErr = upload.file.Close()
	}
	if !upload.removed {
		removeErr = os.Remove(upload.file.Name())
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		upload.removed = removeErr == nil
	}
	return errors.Join(closeErr, removeErr)
}

func stagePolicyUpload(source io.Reader, maxBytes int64) (*policyUpload, error) {
	file, err := os.CreateTemp("", "thinkgo-upload-*")
	if err != nil {
		return nil, fmt.Errorf("stage upload: %w", err)
	}
	staged := &policyUpload{file: file}
	n, copyErr := io.Copy(file, io.LimitReader(source, maxBytes+1))
	if copyErr == nil && n > maxBytes {
		copyErr = ErrUploadTooLarge
	}
	if copyErr != nil {
		return nil, errors.Join(copyErr, staged.Close())
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, errors.Join(err, staged.Close())
	}
	return staged, nil
}
