// Package prompts embeds the scenario configuration: system prompts and official briefing
// templates. This directory is the single source of truth; Version identifies the exact set
// a task ran with.
package prompts

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"sort"
)

//go:embed briefing briefing-templates
var FS embed.FS

// Version is a short content hash over every embedded file.
var Version = func() string {
	h := sha256.New()
	var paths []string
	_ = fs.WalkDir(FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	sort.Strings(paths)
	for _, p := range paths {
		b, _ := FS.ReadFile(p)
		h.Write([]byte(p))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}()
