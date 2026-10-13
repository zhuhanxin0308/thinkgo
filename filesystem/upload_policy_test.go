package filesystem

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func policyTestFile(t *testing.T, name string, content []byte, spill bool) *multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, name))
	// Deliberately untrustworthy: even text and HTML claim to be PNG.
	header.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	memory := int64(1 << 20)
	if spill {
		memory = 0
	}
	form, err := multipart.NewReader(&body, writer.Boundary()).ReadForm(memory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := form.RemoveAll(); err != nil {
			t.Error(err)
		}
	})
	return form.File["file"][0]
}

func policyTestDisk(t *testing.T) (*Local, string) {
	t.Helper()
	root := t.TempDir()
	disk, err := NewLocal(LocalConfig{Root: root, Visibility: VisibilityPrivate})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	return disk, root
}

func policyTestOptions(t *testing.T, limit int64, types map[string][]string) map[string]interface{} {
	t.Helper()
	policy, err := NewUploadPolicy(UploadPolicyConfig{MaxBytes: limit, AllowedTypes: types})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]interface{}{"upload_policy": policy}
}

func policyTestTemp(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, directory)
	}
	return directory
}

func assertNoPolicyTemporary(t *testing.T, roots ...string) {
	t.Helper()
	for _, root := range roots {
		err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if strings.HasPrefix(entry.Name(), "thinkgo-upload-") || strings.HasPrefix(entry.Name(), localTemporaryPrefix) {
				t.Errorf("temporary file leaked: %s", name)
			}
			return nil
		})
		if err != nil {
			t.Error(err)
		}
	}
}

func TestUploadPolicyConfigurationAndImmutablePairs(t *testing.T) {
	invalid := []UploadPolicyConfig{
		{}, {MaxBytes: -1}, {MaxBytes: math.MaxInt64},
		{MaxBytes: 10, AllowedTypes: map[string][]string{"txt": {}}},
		{MaxBytes: 10, AllowedTypes: map[string][]string{"": {"text/plain"}}},
		{MaxBytes: 10, AllowedTypes: map[string][]string{"tar.gz": {"application/gzip"}}},
		{MaxBytes: 10, AllowedTypes: map[string][]string{" txt": {"text/plain"}}},
		{MaxBytes: 10, AllowedTypes: map[string][]string{"txt": {"text/*"}}},
		{MaxBytes: 10, AllowedTypes: map[string][]string{"txt": {"text"}}},
		{MaxBytes: 10, AllowedTypes: map[string][]string{"txt": {"text/plain; charset=utf-8"}}},
		{MaxBytes: 10, AllowedTypes: map[string][]string{"txt": {"text/plain"}, ".TXT": {"text/html"}}},
	}
	for index, config := range invalid {
		if policy, err := NewUploadPolicy(config); policy != nil || !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("invalid config %d accepted: %v", index, err)
		}
	}
	config := UploadPolicyConfig{MaxBytes: 16, AllowedTypes: map[string][]string{".TXT": {"Text/Plain"}}}
	policy, err := NewUploadPolicy(config)
	if err != nil {
		t.Fatal(err)
	}
	config.AllowedTypes[".TXT"][0] = "text/html"
	delete(config.AllowedTypes, ".TXT")
	config.MaxBytes = 1
	disk, _ := policyTestDisk(t)
	file := policyTestFile(t, "note.TXT", []byte("hello"), false)
	key, err := disk.PutFileAs("", file, "saved.txt", map[string]interface{}{"upload_policy": policy})
	if err != nil || key != "saved.txt" {
		t.Fatalf("policy did not snapshot input: %q %v", key, err)
	}
	for _, raw := range []interface{}{nil, (*UploadPolicy)(nil), &UploadPolicy{}, UploadPolicy{}, config, true} {
		options := map[string]interface{}{"upload_policy": raw}
		if key, err := disk.PutFile("", file, nil, options); key != "" || !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("invalid PutFile policy accepted: %T %q %v", raw, key, err)
		}
		if key, err := disk.PutFileAs("", file, "bad.txt", options); key != "" || !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("invalid PutFileAs policy accepted: %T %q %v", raw, key, err)
		}
	}
}

func TestUploadPolicyActualBytesNamesAndMIME(t *testing.T) {
	staging := policyTestTemp(t)
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	types := map[string][]string{"txt": {"text/plain"}, "png": {"image/png"}, "csv": {"text/csv"}}
	for _, spill := range []bool{false, true} {
		for _, rule := range []interface{}{nil, "md5", "sha1", "sha256", "sha384", "sha512", ""} {
			disk, root := policyTestDisk(t)
			file := policyTestFile(t, "photo.PNG", pngData.Bytes(), spill)
			options := policyTestOptions(t, int64(pngData.Len()), types)
			options["visibility"] = VisibilityPrivate
			key, err := disk.PutFile("images", file, rule, options)
			if err != nil || !strings.HasSuffix(key, ".png") {
				t.Fatalf("spill=%t rule=%v key=%q err=%v", spill, rule, key, err)
			}
			if got, err := disk.Read(key); err != nil || got != pngData.String() {
				t.Fatalf("snapshot content changed: %v", err)
			}
			if options["upload_policy"] == nil {
				t.Fatal("options map was mutated")
			}
			assertNoPolicyTemporary(t, staging, root)
		}
	}
	disk, root := policyTestDisk(t)
	options := policyTestOptions(t, 10, types)
	for _, test := range []struct{ original, target, content string }{
		{"x.txt", "ok.txt", "0123456789"},
		{"x.png", "bad.png", "hello"},
		{"x.txt", "bad.csv", "hello"},
		{"x.html", "bad.txt", "hello"},
		{"x.txt", "bad.php.txt", "hello"},
		{"x.txt", ".htaccess", "hello"},
		{"x.txt", "bad.txt.", "hello"},
		{"x.txt", "bad:.txt", "hello"},
		{"x.txt", "bad%00.txt", "hello"},
		{"x.txt", "bad.txt", "01234567890"},
	} {
		file := policyTestFile(t, test.original, []byte(test.content), false)
		file.Size = 0 // Must not bypass the actual stream limit.
		key, err := disk.PutFileAs("", file, test.target, options)
		if test.target == "ok.txt" {
			if err != nil || key != test.target {
				t.Fatalf("exact boundary rejected: %q %v", key, err)
			}
		} else if key != "" || !errors.Is(err, ErrUploadRejected) {
			t.Fatalf("invalid combination accepted: %+v key=%q err=%v", test, key, err)
		}
		assertNoPolicyTemporary(t, staging, root)
	}
	for _, rule := range []interface{}{nil, "sha256"} {
		file := policyTestFile(t, "x.txt", []byte("01234567890"), false)
		file.Size = 1
		if key, err := disk.PutFile("rejected", file, rule, options); key != "" || !errors.Is(err, ErrUploadTooLarge) {
			t.Fatalf("PutFile read limit bypassed: %q %v", key, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "rejected")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected upload created destination directory: %v", err)
	}
	called := false
	file := policyTestFile(t, "x.txt", []byte("hello"), false)
	rule := HashNameRule(func(*multipart.FileHeader) (string, error) { called = true; return "unsafe", nil })
	if _, err := disk.PutFile("", file, rule, options); !errors.Is(err, ErrInvalidConfiguration) || called {
		t.Fatalf("unbounded callback reached: called=%t err=%v", called, err)
	}
	// Legacy calls still require application validation; this feature is opt-in.
	if _, err := disk.PutFileAs("", file, "legacy.html"); err != nil {
		t.Fatalf("legacy contract changed: %v", err)
	}
	assertNoPolicyTemporary(t, staging, root)
}

func TestUploadPolicyFailurePreservesTargetAndCleansSnapshots(t *testing.T) {
	staging := policyTestTemp(t)
	for _, failure := range []string{"size", "MIME", "source-close", "snapshot-close", "copy", "replace"} {
		t.Run(failure, func(t *testing.T) {
			disk, root := policyTestDisk(t)
			if err := disk.Write("saved.txt", "original"); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected upload failure")
			content := "new"
			options := policyTestOptions(t, 10, map[string][]string{"txt": {"text/plain"}})
			switch failure {
			case "size":
				content = strings.Repeat("n", 11)
			case "MIME":
				content = "<html>"
			case "source-close", "snapshot-close":
				disk.operations.closeSource = func(source io.Closer) error {
					_, staged := source.(*policyUpload)
					err := source.Close()
					if (failure == "snapshot-close") == staged {
						return errors.Join(err, injected)
					}
					return err
				}
			case "copy":
				disk.operations.copy = func(writer io.Writer, source io.Reader) (int64, error) {
					n, err := io.CopyN(writer, source, 1)
					return n, errors.Join(err, injected)
				}
			case "replace":
				disk.operations.replace = func(*os.Root, string, string) error { return injected }
			}
			file := policyTestFile(t, "x.txt", []byte(content), false)
			key, err := disk.PutFileAs("", file, "saved.txt", options)
			if key != "" || err == nil || errors.Is(err, ErrFilesystemOperationCommitted) {
				t.Fatalf("failed upload published: key=%q err=%v", key, err)
			}
			if failure != "size" && failure != "MIME" && !errors.Is(err, injected) {
				t.Fatalf("lost source cause: %v", err)
			}
			if got, err := disk.Read("saved.txt"); err != nil || got != "original" {
				t.Fatalf("old target changed: %q %v", got, err)
			}
			assertNoPolicyTemporary(t, staging, root)
		})
	}
}

func TestUploadPolicyHashAndWriteUseSameSnapshot(t *testing.T) {
	staging := policyTestTemp(t)
	disk, root := policyTestDisk(t)
	original := "original upload bytes"
	file := policyTestFile(t, "x.txt", []byte(original), true)
	changed := false
	disk.operations.closeSource = func(source io.Closer) error {
		if backing, ok := source.(*os.File); ok {
			name := backing.Name()
			if err := backing.Close(); err != nil {
				return err
			}
			changed = true
			return os.WriteFile(name, []byte("changed after snapshot"), 0o600)
		}
		return source.Close()
	}
	replace := disk.operations.replace
	disk.operations.replace = func(root *os.Root, source, target string) error {
		assertNoPolicyTemporary(t, staging)
		return replace(root, source, target)
	}
	key, err := disk.PutFile("", file, "sha256", policyTestOptions(t, 100, map[string][]string{"txt": {"text/plain"}}))
	if err != nil || !changed {
		t.Fatalf("snapshot mutation setup failed: changed=%t err=%v", changed, err)
	}
	digest := sha256.Sum256([]byte(original))
	text := hex.EncodeToString(digest[:])
	if key != text[:2]+"/"+text[2:]+".txt" {
		t.Fatalf("name did not hash original snapshot: %s", key)
	}
	if got, err := disk.Read(key); err != nil || got != original {
		t.Fatalf("written bytes diverged from hash: %q %v", got, err)
	}
	assertNoPolicyTemporary(t, staging, root)
}

type policyCountingReader struct{ remaining, read int64 }

func (source *policyCountingReader) Read(buffer []byte) (int, error) {
	if source.remaining == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(buffer)), source.remaining)
	for index := range buffer[:int(n)] {
		buffer[index] = 'a'
	}
	source.remaining -= n
	source.read += n
	return int(n), nil
}

type policyErrorReader struct{ err error }

func (source policyErrorReader) Read([]byte) (int, error) { return 0, source.err }

func TestUploadPolicyBoundedReadsAndReaderFailures(t *testing.T) {
	staging := policyTestTemp(t)
	source := &policyCountingReader{remaining: 1 << 20}
	if file, err := stagePolicyUpload(source, 16); file != nil || !errors.Is(err, ErrUploadTooLarge) || source.read != 17 {
		t.Fatalf("expected one byte oversize probe: read=%d file=%v err=%v", source.read, file, err)
	}
	injected := errors.New("read failed")
	if file, err := stagePolicyUpload(io.MultiReader(strings.NewReader("abc"), policyErrorReader{injected}), 16); file != nil || !errors.Is(err, injected) {
		t.Fatalf("read error accepted: %v %v", file, err)
	}
	file, err := stagePolicyUpload(strings.NewReader("exact"), 5)
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.file.Stat()
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot permissions: %v %v", info, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("duplicate close: %v", err)
	}
	assertNoPolicyTemporary(t, staging)
}

func TestUploadPolicyConcurrentReuseAndClosedDisk(t *testing.T) {
	disk, _ := policyTestDisk(t)
	options := policyTestOptions(t, 100, map[string][]string{"txt": {"text/plain"}})
	file := policyTestFile(t, "x.txt", []byte("shared immutable content"), false)
	var group sync.WaitGroup
	for index := range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			name := fmt.Sprintf("file-%d.txt", index)
			if _, err := disk.PutFileAs("", file, name, options); err != nil {
				t.Error(err)
			}
			if got, err := disk.Read(name); err != nil || got != "shared immutable content" {
				t.Errorf("concurrent content: %q %v", got, err)
			}
		}()
	}
	group.Wait()
	if err := disk.Close(); err != nil {
		t.Fatal(err)
	}
	for _, current := range []*Local{nil, {}, disk} {
		if key, err := current.PutFileAs("", file, "closed.txt", options); key != "" || err == nil {
			t.Fatalf("invalid disk accepted: %q %v", key, err)
		}
	}
}

func FuzzUploadPolicyStagingByteLimit(f *testing.F) {
	f.Add([]byte("abc"), uint8(3))
	f.Add([]byte("longer"), uint8(1))
	f.Add([]byte{}, uint8(0))
	f.Fuzz(func(t *testing.T, content []byte, size uint8) {
		if len(content) > 2048 {
			return
		}
		limit := int64(size) + 1
		file, err := stagePolicyUpload(bytes.NewReader(content), limit)
		if int64(len(content)) > limit {
			if file != nil || !errors.Is(err, ErrUploadTooLarge) {
				t.Fatalf("oversize accepted: size=%d limit=%d err=%v", len(content), limit, err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		}()
		got, err := io.ReadAll(file.file)
		if err != nil || !bytes.Equal(got, content) {
			t.Fatalf("valid upload truncated: %v", err)
		}
	})
}

func TestUploadPolicyAdmissionAndCommittedErrorBoundaries(t *testing.T) {
	staging := policyTestTemp(t)
	disk, root := policyTestDisk(t)
	options := policyTestOptions(t, 100, map[string][]string{"txt": {"text/plain"}, "log": {"text/plain"}})
	file := policyTestFile(t, "report.txt", []byte("entry"), false)
	if key, err := disk.PutFileAs("reports", file, "entry.log", options); err != nil || key != "reports/entry.log" {
		t.Fatalf("allowed extension alias rejected: %q %v", key, err)
	}
	empty := policyTestFile(t, "empty.txt", nil, false)
	if key, err := disk.PutFileAs("", empty, "empty.txt", options); err != nil || key != "empty.txt" {
		t.Fatalf("empty text contract: %q %v", key, err)
	}
	if got, err := disk.Read("empty.txt"); err != nil || got != "" {
		t.Fatalf("empty upload did not persist: %q %v", got, err)
	}
	for _, test := range []struct {
		name    string
		invoke  func() (string, error)
		wantErr error
	}{
		{"traversal", func() (string, error) { return disk.PutFile("../outside", file, "sha256", options) }, ErrInvalidPath},
		{"nil-input", func() (string, error) { return disk.PutFileAs("", nil, "missing.txt", options) }, ErrInvalidConfiguration},
		{"invalid-rule", func() (string, error) { return disk.PutFile("", file, "invalid-hash", options) }, ErrInvalidConfiguration},
	} {
		t.Run(test.name, func(t *testing.T) {
			key, err := test.invoke()
			if key != "" || !errors.Is(err, test.wantErr) {
				t.Fatalf("invalid admission accepted: %q %v", key, err)
			}
			assertNoPolicyTemporary(t, staging, root)
		})
	}
	// FileHeader.Open fails before staging; a fabricated header must not publish.
	if key, err := disk.PutFileAs("", &multipart.FileHeader{Filename: "missing.txt"}, "missing.txt", options); key != "" || err == nil {
		t.Fatalf("missing source accepted: %q %v", key, err)
	}
	// A staging failure must still close the already-open source.
	closed := 0
	disk.operations.closeSource = func(source io.Closer) error {
		closed++
		return source.Close()
	}
	missing := filepath.Join(staging, "nonexistent")
	for _, variable := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(variable, missing)
	}
	key, err := disk.PutFileAs("", file, "staging-failure.txt", options)
	if key != "" || err == nil || closed != 1 {
		t.Fatalf("failed staging did not release source: %q %v closes=%d", key, err, closed)
	}
	for _, variable := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(variable, staging)
	}
	// Once publication occurs, preserve the existing committed-error contract.
	injected := errors.New("directory durability failure")
	disk.operations.syncDirectory = func(*os.Root, string) error { return injected }
	key, err = disk.PutFileAs("", file, "committed.txt", options)
	if key != "" || !errors.Is(err, ErrFilesystemOperationCommitted) || !errors.Is(err, injected) {
		t.Fatalf("lost committed outcome: %q %v", key, err)
	}
	if got, err := disk.Read("committed.txt"); err != nil || got != "entry" {
		t.Fatalf("committed data missing: %q %v", got, err)
	}
	assertNoPolicyTemporary(t, staging, root)
}

func TestUploadPolicyOversizeNeverPublishes(t *testing.T) {
	const content = "01234567890"
	for _, generated := range []bool{false, true} {
		disk, _ := policyTestDisk(t)
		file := policyTestFile(t, "x.txt", []byte(content), false)
		file.Size = 1
		target := "saved.txt"
		if generated {
			digest := sha256.Sum256([]byte(content))
			hexValue := hex.EncodeToString(digest[:])
			target = hexValue[:2] + "/" + hexValue[2:] + ".txt"
		}
		if err := disk.Write(target, "old data"); err != nil {
			t.Fatal(err)
		}
		options := policyTestOptions(t, 10, map[string][]string{"txt": {"text/plain"}})
		var key string
		var err error
		if generated {
			key, err = disk.PutFile("", file, "sha256", options)
		} else {
			key, err = disk.PutFileAs("", file, target, options)
		}
		data, readErr := disk.Read(target)
		if key != "" || !errors.Is(err, ErrUploadTooLarge) || readErr != nil || data != "old data" {
			t.Errorf("generated=%t: key=%q err=%v stored=%q readErr=%v; oversize input must not replace target", generated, key, err, data, readErr)
		}
	}
}
