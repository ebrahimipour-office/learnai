package topics

import (
	"archive/zip"
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"path"

	"learninghub/internal/domain"
	"learninghub/internal/fsx"
)

// Dir returns topic id's folder relative to the base dir (topics/<dir_name>); ErrNotFound when the
// topic is unknown or its folder does not exist yet (nothing generated).
func (s *Service) Dir(ctx context.Context, id int64) (string, error) {
	var name string
	err := s.db.Reader().QueryRowContext(ctx, `SELECT dir_name FROM topics WHERE id = ?`, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	dir := path.Join("topics", name)
	if err := s.root.EnsureRealDir(dir); err != nil {
		return "", err
	}
	if info, err := s.root.Lstat(dir); errors.Is(err, fs.ErrNotExist) || (err == nil && !info.IsDir()) {
		return "", domain.ErrNotFound
	} else if err != nil {
		return "", err
	}
	return dir, nil
}

// Zip writes dir's regular files to w as a zip, named relative to dir's parent so the archive
// unpacks into one folder (T10.20). .internal/ (rubrics) and symlinks are left out; reads go through fsx.Root.
func (s *Service) Zip(dir string, w io.Writer) error {
	zw := zip.NewWriter(w)
	parent := path.Dir(dir)
	err := fs.WalkDir(s.root.FS(), dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".internal" {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || fsx.IsTempName(d.Name()) {
			return nil // symlink, device or a write's temp file: never exported
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name, h.Method = p[len(parent)+1:], zip.Deflate
		f, err := s.root.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		zf, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		_, err = io.Copy(zf, f)
		return err
	})
	if err != nil {
		return err
	}
	return zw.Close()
}
