package controllers

import (
	"archive/zip"
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"testing"
)

func zipHeader(t *testing.T, files map[string]string) *multipart.FileHeader {
	t.Helper()
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
	}
	zw.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("archive", "pool.zip")
	fw.Write(zbuf.Bytes())
	mw.Close()

	req := httptest.NewRequest("POST", "/", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	return req.MultipartForm.File["archive"][0]
}

func TestReadPoolArchiveStripsDirectoriesAndReadsManifest(t *testing.T) {
	fh := zipHeader(t, map[string]string{
		"../../etc/evil.bin":  "a",
		"sub/flag{b}.bin":     "b",
		"__MACOSX/._evil.bin": "x",
		"manifest.json":       `{"evil.bin": "flag{a}"}`,
	})

	files, manifest, err := readPoolArchive(fh)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	for _, f := range files {
		if f.name != "evil.bin" && f.name != "flag{b}.bin" {
			t.Fatalf("directory not stripped: %q", f.name)
		}
	}
	if manifest["evil.bin"] != "flag{a}" {
		t.Fatalf("manifest not parsed: %v", manifest)
	}
}

func TestReadPoolArchiveRejectsDuplicateNames(t *testing.T) {
	fh := zipHeader(t, map[string]string{"a/x.bin": "1", "b/x.bin": "2"})
	if _, _, err := readPoolArchive(fh); err == nil {
		t.Fatal("expected duplicate name error")
	}
}
