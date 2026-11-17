package httpserver

import (
	"errors"
	"html/template"
	"io"
	"sync"
	"time"

	"learninghub/internal/fsx"
	"learninghub/internal/render"
)

// errTooLarge: the file is bigger than the display cap; the page says so instead of loading it (T10.6).
var errTooLarge = errors.New("file too large to display")

const tooLargeMsg = "فایل بزرگ‌تر از حد نمایش است: "

// rendered is one Markdown file as the panel shows it.
type rendered struct {
	HTML template.HTML
	TOC  []render.Heading
}

// mdKey changes whenever the file is rewritten or hand-edited, so entries never need invalidating.
// Size+mtime rather than files.sha256: the DB hash is only refreshed by sync, a hand edit is not.
type mdKey struct {
	rel  string
	size int64
	mod  time.Time
}

// mdCache renders Markdown files read through root, at most max bytes each (T10.6).
type mdCache struct {
	root    *fsx.Root
	max     int64
	mu      sync.Mutex
	entries map[mdKey]rendered
	renders int // cache misses, for tests
}

const mdCacheSize = 64

// fileError is the page notice for a file get could not show; missing is the "not found on disk" text.
func fileError(err error, missing, rel string) string {
	if errors.Is(err, errTooLarge) {
		return tooLargeMsg + rel
	}
	return missing + rel
}

// get returns the rendered file. Errors are fs errors (missing file) or errTooLarge.
func (c *mdCache) get(rel string) (rendered, error) {
	f, err := c.root.Open(rel)
	if err != nil {
		return rendered{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return rendered{}, err
	}
	if fi.Size() > c.max {
		return rendered{}, errTooLarge
	}
	k := mdKey{rel, fi.Size(), fi.ModTime()}
	c.mu.Lock()
	r, ok := c.entries[k]
	c.mu.Unlock()
	if ok {
		return r, nil
	}
	b, err := io.ReadAll(io.LimitReader(f, c.max+1)) // the file may grow after Stat
	if err != nil {
		return rendered{}, err
	}
	if int64(len(b)) > c.max {
		return rendered{}, errTooLarge
	}
	r = rendered{render.Markdown(b), render.TOC(b)}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= mdCacheSize {
		clear(c.entries) // shortcut: drops everything instead of LRU; fine for one local user, upgrade if pages get many files
	}
	c.entries[k] = r
	c.renders++
	return r, nil
}
