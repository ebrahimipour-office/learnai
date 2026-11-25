package topics_test

import (
	"archive/zip"
	"bytes"
	"testing"

	"learninghub/internal/fsx"
	"learninghub/internal/services/topics"
)

// T13.4: a write's temp file is never exported.
func TestZipSkipsTempFiles(t *testing.T) {
	root, err := fsx.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, p := range []string{"topics/01-x/a.md", "topics/01-x/.a.md.tmp-0123456789abcdef"} {
		if err := root.WriteAtomic(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := topics.NewService(nil, root).Zip("topics/01-x", &buf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "01-x/a.md" {
		for _, f := range zr.File {
			t.Log(f.Name)
		}
		t.Fatal("zip should hold only 01-x/a.md")
	}
}
