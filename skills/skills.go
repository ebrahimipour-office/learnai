// Package skills embeds the default AGENTS.md, profile.md and skills, and installs them into the
// base dir on first run (TDD §6.3). Files the user already has are never overwritten.
package skills

import (
	"embed"
	"errors"
	"io/fs"
	"path"
	"strings"

	"learninghub/internal/fsx"
)

//go:embed *.tmpl */SKILL.md
var FS embed.FS

// SkillRel is where a skill lives in the base dir.
func SkillRel(name string) string { return path.Join(".agent", name, "SKILL.md") }

// Install copies every embedded file that is missing from the base dir and reports how many it wrote.
func Install(root *fsx.Root) (int, error) {
	n := 0
	err := fs.WalkDir(FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		dst := strings.TrimSuffix(p, ".tmpl") // AGENTS.md.tmpl → AGENTS.md
		if path.Base(p) == "SKILL.md" {
			dst = SkillRel(path.Dir(p))
		}
		b, err := FS.ReadFile(p)
		if err != nil {
			return err
		}
		if err := root.WriteNew(dst, b, 0o644); errors.Is(err, fsx.ErrFileExists) {
			return nil // user's copy wins
		} else if err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}
