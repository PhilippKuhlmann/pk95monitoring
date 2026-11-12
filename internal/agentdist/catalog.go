// Package agentdist is the server side of the agent distribution: the catalog of agent
// binaries (with SHA-256) and the install scripts and one-liners shown in the web UI.
package agentdist

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Binary is one agent build offered for download.
type Binary struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	File      string `json:"file"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"` // lower-case hex
	Available bool   `json:"available"`
}

// Targets are the platforms the server distributes, in display order.
var Targets = []struct{ OS, Arch string }{
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"windows", "amd64"},
}

// FileName is the file of a build in the agent directory, e.g. pk95agent-windows-amd64.exe.
func FileName(goos, goarch string) string {
	name := "pk95agent-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

type cached struct {
	size  int64
	mtime time.Time
	sum   string
}

// Catalog lists the agent binaries in a directory. Hashes are cached by size and mtime, so
// replacing a file (new image, manual copy) is picked up on the next call.
type Catalog struct {
	dir     string
	version string

	mu    sync.Mutex
	cache map[string]cached
}

// NewCatalog serves the binaries in dir; version is the agent version they were built as
// (the server version).
func NewCatalog(dir, version string) *Catalog {
	return &Catalog{dir: dir, version: version, cache: map[string]cached{}}
}

// Version is the agent version the server distributes.
func (c *Catalog) Version() string { return c.version }

// Binaries lists every target; missing files have Available false.
func (c *Catalog) Binaries() []Binary {
	out := make([]Binary, 0, len(Targets))
	for _, t := range Targets {
		b, _ := c.binary(t.OS, t.Arch)
		out = append(out, b)
	}
	return out
}

// Lookup returns the binary for a platform and its path; ok is false when the platform is
// not distributed or the file is missing.
func (c *Catalog) Lookup(goos, goarch string) (Binary, string, bool) {
	for _, t := range Targets {
		if t.OS == goos && t.Arch == goarch {
			b, path := c.binary(goos, goarch)
			return b, path, b.Available
		}
	}
	return Binary{}, "", false
}

func (c *Catalog) binary(goos, goarch string) (Binary, string) {
	b := Binary{OS: goos, Arch: goarch, File: FileName(goos, goarch)}
	path := filepath.Join(c.dir, b.File)
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
		return b, path
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[b.File]
	if !ok || e.size != st.Size() || !e.mtime.Equal(st.ModTime()) {
		sum, err := hashFile(path)
		if err != nil {
			delete(c.cache, b.File)
			return b, path
		}
		e = cached{size: st.Size(), mtime: st.ModTime(), sum: sum}
		c.cache[b.File] = e
	}
	b.Size, b.SHA256, b.Available = e.size, e.sum, true
	return b, path
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
