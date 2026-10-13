package filesystem_test

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"os"

	"github.com/zhuhanxin0308/thinkgo/v3/filesystem"
)

func ExampleLocal_PutFileAs_uploadPolicy() {
	policy, err := filesystem.NewUploadPolicy(filesystem.UploadPolicyConfig{
		MaxBytes: 1 << 20,
		AllowedTypes: map[string][]string{
			"txt": {"text/plain"},
		},
	})
	if err != nil {
		panic(err)
	}
	root, err := os.MkdirTemp("", "thinkgo-private-upload-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)
	disk, err := filesystem.NewLocal(filesystem.LocalConfig{Root: root, Visibility: filesystem.VisibilityPrivate})
	if err != nil {
		panic(err)
	}
	defer disk.Close()

	// Fixture only: real handlers also limit the request before multipart parsing,
	// authenticate and authorize the upload, and clean up parsed multipart files.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "client-report.txt")
	if err != nil {
		panic(err)
	}
	if _, err := part.Write([]byte("report contents")); err != nil {
		panic(err)
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	form, err := multipart.NewReader(&body, writer.Boundary()).ReadForm(1 << 20)
	if err != nil {
		panic(err)
	}
	defer form.RemoveAll()
	key, err := disk.PutFileAs("reports", form.File["file"][0], "reviewed.txt", map[string]interface{}{
		"upload_policy": policy,
	})
	if err != nil {
		panic(err)
	}
	// This is a storage reference, not a public URL or an authorization credential.
	fmt.Println(key)
	// Output: reports/reviewed.txt
}
