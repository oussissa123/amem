// Package docstore is the canonical storage: one directory per project with
// Markdown + YAML front matter files and immutable history (spec 02 §4).
package docstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/oussissa/amem/internal/domain"
)

type Store struct {
	Root string // parent directory of all project directories
}

func New(root string) *Store { return &Store{Root: root} }

func (s *Store) ProjectDir(projectID string) string { return filepath.Join(s.Root, projectID) }

// RelPath is the knowledge file path relative to the project directory.
func RelPath(t domain.Type, key string) string {
	return filepath.Join("knowledge", t.Dir(), key+".md")
}

func historyRel(key string, version int) string {
	return filepath.Join("history", key, fmt.Sprintf("v%04d.md", version))
}

// InitProject creates the directory tree and project.yaml.
func (s *Store) InitProject(p *domain.Project) error {
	dir := s.ProjectDir(p.ID)
	if _, err := os.Stat(filepath.Join(dir, "project.yaml")); err == nil {
		return domain.Errf(domain.CodeProjectExists, "project directory already initialised: "+dir)
	}
	for _, sub := range []string{"history", "sessions", "indexes", "knowledge/.deleted"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o750); err != nil {
			return err
		}
	}
	for _, t := range domain.AllTypes() {
		if err := os.MkdirAll(filepath.Join(dir, "knowledge", t.Dir()), 0o750); err != nil {
			return err
		}
	}
	return s.WriteProject(p)
}

func (s *Store) WriteProject(p *domain.Project) error {
	data, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	return WriteAtomic(filepath.Join(s.ProjectDir(p.ID), "project.yaml"), data)
}

func (s *Store) ReadProject(projectID string) (*domain.Project, []byte, error) {
	data, err := os.ReadFile(filepath.Join(s.ProjectDir(projectID), "project.yaml"))
	if err != nil {
		return nil, nil, err
	}
	var p domain.Project
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, nil, fmt.Errorf("project.yaml: %w", err)
	}
	return &p, data, nil
}

// Serialize renders a knowledge document.
func Serialize(k *domain.Knowledge) ([]byte, error) {
	k.Schema = domain.SchemaKnowledge
	front, err := yaml.Marshal(k)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("---\n")
	b.Write(front)
	b.WriteString("---\n")
	if body := strings.TrimSpace(k.Body); body != "" {
		b.WriteString("\n")
		b.WriteString(body)
		b.WriteString("\n")
	}
	return b.Bytes(), nil
}

// Parse reads a knowledge document.
func Parse(data []byte) (*domain.Knowledge, error) {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return nil, errors.New("missing front matter")
	}
	front, body, ok := strings.Cut(s[4:], "\n---\n")
	if !ok {
		if strings.HasSuffix(s[4:], "\n---") {
			front, body = strings.TrimSuffix(s[4:], "\n---"), ""
		} else {
			return nil, errors.New("unterminated front matter")
		}
	}
	var k domain.Knowledge
	if err := yaml.Unmarshal([]byte(front), &k); err != nil {
		return nil, fmt.Errorf("front matter: %w", err)
	}
	k.Body = strings.TrimSpace(body)
	k.CreatedAt, k.UpdatedAt = k.CreatedAt.UTC(), k.UpdatedAt.UTC()
	return &k, nil
}

func Hash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// ReadFile returns the current document and the hash of its bytes.
func (s *Store) ReadFile(projectID, rel string) (*domain.Knowledge, string, error) {
	data, err := os.ReadFile(filepath.Join(s.ProjectDir(projectID), rel))
	if err != nil {
		return nil, "", err
	}
	k, err := Parse(data)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", rel, err)
	}
	return k, Hash(data), nil
}

func (s *Store) ReadVersion(projectID, key string, version int) (*domain.Knowledge, error) {
	k, _, err := s.ReadFile(projectID, historyRel(key, version))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, domain.NotFound(fmt.Sprintf("%s version %d", key, version))
	}
	return k, err
}

// Write persists a new version (spec 02 §5.2 steps 9–10): the immutable
// history copy first, then the main file atomically. It returns the main
// file's relative path, its hash and an undo function restoring the previous
// main file if the surrounding transaction fails.
func (s *Store) Write(k *domain.Knowledge) (rel, hash string, undo func(), err error) {
	data, err := Serialize(k)
	if err != nil {
		return "", "", nil, err
	}
	dir := s.ProjectDir(k.ProjectID)
	hist := filepath.Join(dir, historyRel(k.Key, k.Version))
	if err := os.MkdirAll(filepath.Dir(hist), 0o750); err != nil {
		return "", "", nil, err
	}
	if err := writeExclusive(hist, data); err != nil {
		return "", "", nil, err
	}
	rel = RelPath(k.Type, k.Key)
	if k.DeletedAt != nil {
		rel = filepath.Join("knowledge", ".deleted", k.Key+".md")
	}
	main := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(main), 0o750); err != nil {
		return "", "", nil, err
	}
	if err := WriteAtomic(main, data); err != nil {
		os.Remove(hist)
		return "", "", nil, err
	}
	live := filepath.Join(dir, RelPath(k.Type, k.Key))
	if k.DeletedAt != nil {
		os.Remove(live)
	}
	prevVersion := k.Version - 1
	undo = func() {
		os.Remove(hist)
		if prevVersion < 1 {
			os.Remove(main)
			return
		}
		if prev, err := os.ReadFile(filepath.Join(dir, historyRel(k.Key, prevVersion))); err == nil {
			_ = WriteAtomic(live, prev)
			if k.DeletedAt != nil {
				os.Remove(main)
			}
		}
	}
	return rel, Hash(data), undo, nil
}

// Walk calls fn for every live knowledge file of a project.
func (s *Store) Walk(projectID string, fn func(rel string, k *domain.Knowledge, hash string) error) error {
	dir := s.ProjectDir(projectID)
	for _, t := range domain.AllTypes() {
		sub := filepath.Join(dir, "knowledge", t.Dir())
		entries, err := os.ReadDir(sub)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			rel := filepath.Join("knowledge", t.Dir(), e.Name())
			k, hash, err := s.ReadFile(projectID, rel)
			if err != nil {
				return err
			}
			if err := fn(rel, k, hash); err != nil {
				return err
			}
		}
	}
	return nil
}

// AllKeys lists every key ever used in a project (live, deleted, history), for RG-01.
func (s *Store) AllKeys(projectID string) ([]string, error) {
	var keys []string
	dir := s.ProjectDir(projectID)
	entries, err := os.ReadDir(filepath.Join(dir, "history"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() && domain.ValidKey(e.Name()) {
			keys = append(keys, e.Name())
		}
	}
	return keys, nil
}

// WriteSession stores a (redacted) raw material under sessions/<date>/.
func (s *Store) WriteSession(projectID, id string, at time.Time, text string) (string, error) {
	rel := filepath.Join("sessions", at.UTC().Format("2006-01-02"), id+".md")
	path := filepath.Join(s.ProjectDir(projectID), rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", err
	}
	return rel, WriteAtomic(path, []byte(text))
}

// WriteGenerated writes a derived file (PROJECT_MAP.md, indexes/…).
func (s *Store) WriteGenerated(projectID, rel string, data []byte) error {
	path := filepath.Join(s.ProjectDir(projectID), rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return WriteAtomic(path, data)
}

// WriteAtomic writes via a temporary file, fsync and rename (ENF-04).
func WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o640); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(dir)
}

func writeExclusive(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
