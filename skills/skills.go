// Package skills embeds the default AGENTS.md, profile.md, suggested .gitignore and skills, and installs them into the
// base dir on first run (TDD §6.3). Files the user already has are never overwritten.
package skills

import (
	"embed"
	"errors"
	"io/fs"
	"path"
	"strconv"
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
		if dst == "gitignore" {
			dst = ".gitignore" // a dotfile is not matched by //go:embed *.tmpl
		}
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

// Update is an installed skill older than the embedded one (TDD §6.3, §13.2): the settings page offers it
// and Upgrade applies it only when the user asks.
type Update struct{ Name, Installed, Embedded string }

// Outdated lists installed skills whose frontmatter version is lower than the embedded one, by name.
// A missing or unparsable installed file is skipped (Install restores a missing one).
func Outdated(root *fsx.Root) ([]Update, error) {
	names, err := fs.Glob(FS, "*/SKILL.md")
	if err != nil {
		return nil, err
	}
	var out []Update
	for _, p := range names {
		name := path.Dir(p)
		emb, _ := FS.ReadFile(p)
		inst, err := root.ReadFile(SkillRel(name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		if iv, ev := Version(inst), Version(emb); iv != "" && older(iv, ev) {
			out = append(out, Update{Name: name, Installed: iv, Embedded: ev})
		}
	}
	return out, nil
}

// Upgrade copies the installed SKILL.md of an outdated skill to SKILL.<version>.bak.md (WriteNew, never
// overwritten), then replaces it with the embedded one.
func Upgrade(root *fsx.Root, name string) error {
	ups, err := Outdated(root)
	if err != nil {
		return err
	}
	for _, u := range ups {
		if u.Name != name {
			continue
		}
		cur, err := root.ReadFile(SkillRel(name))
		if err != nil {
			return err
		}
		if err := root.WriteNew(path.Join(".agent", name, "SKILL."+u.Installed+".bak.md"), cur, 0o644); err != nil {
			return err
		}
		emb, err := FS.ReadFile(name + "/SKILL.md")
		if err != nil {
			return err
		}
		return root.WriteAtomic(SkillRel(name), emb, 0o644)
	}
	return ErrNotOutdated
}

// ErrNotOutdated means Upgrade found no newer embedded version of that skill.
var ErrNotOutdated = errors.New("skill is not outdated")

// Version is the frontmatter version of a SKILL.md, or "".
func Version(b []byte) string {
	fm, _, ok := strings.Cut(strings.TrimPrefix(strings.ReplaceAll(string(b), "\r\n", "\n"), "\xef\xbb\xbf"), "\n---\n")
	if !ok || !strings.HasPrefix(fm, "---\n") {
		return ""
	}
	for _, l := range strings.Split(fm, "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "version" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// older compares dotted numeric versions (1.10.0 > 1.9.0); a non-numeric part counts as 0.
func older(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(as), len(bs)) {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}
