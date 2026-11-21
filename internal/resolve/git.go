// Package resolve finds the Git remotes around a directory, without running
// git (spec 02 §12.1 step 2).
package resolve

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/oussissa/amem/internal/domain"
)

// RemotesAround returns the normalized remotes of the repository containing
// dir; if dir is not inside a repository, those of its immediate
// subdirectories (a Multica task may check out several repositories).
func RemotesAround(dir string) []string {
	if dir == "" {
		return nil
	}
	dir, _ = filepath.Abs(dir)
	for d := dir; ; d = filepath.Dir(d) {
		if cfg := gitConfigPath(d); cfg != "" {
			return normalized(readRemotes(cfg))
		}
		if parent := filepath.Dir(d); parent == d {
			break
		}
	}
	var all []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if cfg := gitConfigPath(filepath.Join(dir, e.Name())); cfg != "" {
			all = append(all, readRemotes(cfg)...)
		}
	}
	return normalized(all)
}

// gitConfigPath returns the config file of the repository rooted at d, if any.
// Handles plain repositories and worktrees (.git file with gitdir: + commondir).
func gitConfigPath(d string) string {
	gitPath := filepath.Join(d, ".git")
	fi, err := os.Stat(gitPath)
	if err != nil {
		return ""
	}
	if fi.IsDir() {
		return filepath.Join(gitPath, "config")
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return ""
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return ""
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(d, gitdir)
	}
	if common, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		c := strings.TrimSpace(string(common))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitdir, c)
		}
		return filepath.Join(c, "config")
	}
	return filepath.Join(gitdir, "config")
}

func readRemotes(cfg string) []string {
	f, err := os.Open(cfg)
	if err != nil {
		return nil
	}
	defer f.Close()
	var urls []string
	inRemote := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			inRemote = strings.HasPrefix(line, "[remote ")
			continue
		}
		if !inRemote {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "url" {
			urls = append(urls, strings.TrimSpace(v))
		}
	}
	return urls
}

func normalized(urls []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range urls {
		n := domain.NormalizeRemote(u)
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
