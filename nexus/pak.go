package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

type PakFileEntry struct {
	Name   string
	Offset int64
	Size   int64
}

// readPakContents reads a Quake .pak file and returns directory entries.
// It validates offsets/sizes against the underlying file to avoid out-of-bounds reads.
func readPakContents(pakPath string) ([]PakFileEntry, error) {
	f, err := os.Open(pakPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	fileSize := st.Size()
	if fileSize < 12 {
		return nil, fmt.Errorf("pak too small: %d", fileSize)
	}

	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return nil, err
	}

	if string(hdr[0:4]) != "PACK" {
		return nil, fmt.Errorf("invalid pak magic: %q", string(hdr[0:4]))
	}

	dirOffset := int64(int32(binary.LittleEndian.Uint32(hdr[4:8])))
	dirLen := int64(int32(binary.LittleEndian.Uint32(hdr[8:12])))
	if dirOffset < 0 || dirLen < 0 {
		return nil, fmt.Errorf("invalid pak directory offset/len: %d/%d", dirOffset, dirLen)
	}
	if dirOffset+dirLen > fileSize {
		return nil, fmt.Errorf("pak directory out of bounds: off=%d len=%d size=%d", dirOffset, dirLen, fileSize)
	}
	if dirLen%64 != 0 {
		return nil, fmt.Errorf("invalid pak directory length: %d (not multiple of 64)", dirLen)
	}

	if _, err := f.Seek(dirOffset, io.SeekStart); err != nil {
		return nil, err
	}

	dirBytes := make([]byte, dirLen)
	if _, err := io.ReadFull(f, dirBytes); err != nil {
		return nil, err
	}

	out := make([]PakFileEntry, 0, dirLen/64)
	for i := int64(0); i < dirLen; i += 64 {
		chunk := dirBytes[i : i+64]
		rawName := chunk[0:56]
		if nul := bytes.IndexByte(rawName, 0); nul >= 0 {
			rawName = rawName[:nul]
		}
		originalName := string(rawName)
		if originalName == "" {
			continue
		}
		name, err := cleanVFSPath(originalName)
		if err != nil {
			return nil, fmt.Errorf("invalid pak entry name %q: %w", originalName, err)
		}

		off := int64(int32(binary.LittleEndian.Uint32(chunk[56:60])))
		sz := int64(int32(binary.LittleEndian.Uint32(chunk[60:64])))
		if off < 0 || sz < 0 {
			return nil, fmt.Errorf("invalid entry offset/size for %q: %d/%d", name, off, sz)
		}
		if off+sz > fileSize {
			return nil, fmt.Errorf("entry out of bounds for %q: off=%d size=%d file=%d", name, off, sz, fileSize)
		}

		out = append(out, PakFileEntry{Name: name, Offset: off, Size: sz})
	}

	return out, nil
}

type pakIndex struct {
	modTimeUnixNano int64
	fileSize        int64
	entries         map[string]PakFileEntry // keyed by normalized (lowercased) name
}

type pakIndexCache struct {
	mu     sync.Mutex
	byPath map[string]*pakIndex
}

func newPakIndexCache() *pakIndexCache {
	return &pakIndexCache{byPath: make(map[string]*pakIndex)}
}

func (c *pakIndexCache) Get(pakPath string) (*pakIndex, error) {
	st, err := os.Stat(pakPath)
	if err != nil {
		return nil, err
	}
	mt := st.ModTime().UnixNano()
	sz := st.Size()

	c.mu.Lock()
	if existing := c.byPath[pakPath]; existing != nil && existing.modTimeUnixNano == mt && existing.fileSize == sz {
		c.mu.Unlock()
		return existing, nil
	}
	c.mu.Unlock()

	contents, err := readPakContents(pakPath)
	if err != nil {
		return nil, err
	}
	m := make(map[string]PakFileEntry, len(contents))
	for _, e := range contents {
		key := normalizeVFSKey(e.Name)
		if key == "" {
			continue
		}
		m[key] = e
	}

	idx := &pakIndex{
		modTimeUnixNano: mt,
		fileSize:        sz,
		entries:         m,
	}

	c.mu.Lock()
	c.byPath[pakPath] = idx
	c.mu.Unlock()
	return idx, nil
}

func newPakExtractHandler(dataDir string, pakCache *pakIndexCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		rest := strings.TrimPrefix(r.URL.Path, "/pak-extract/")
		rest = strings.Trim(rest, "/")
		parts := strings.Split(rest, "/")
		if len(parts) < 4 {
			http.NotFound(w, r)
			return
		}

		mod := parts[0]
		layer := parts[1]
		pakName := parts[2]
		internal := strings.Join(parts[3:], "/")

		if mod == "" || strings.Contains(mod, "..") || strings.ContainsAny(mod, `/\`) {
			http.Error(w, "invalid mod", http.StatusBadRequest)
			return
		}
		if layer != "common" && layer != "client" {
			http.Error(w, "invalid layer", http.StatusBadRequest)
			return
		}
		if pakName == "" || strings.Contains(pakName, "..") || strings.ContainsAny(pakName, `/\`) {
			http.Error(w, "invalid pak", http.StatusBadRequest)
			return
		}

		key := normalizeVFSKey(internal)
		if key == "" {
			http.Error(w, "invalid file", http.StatusBadRequest)
			return
		}

		pakPath := filepath.Join(dataDir, mod, layer, pakName)
		idx, err := pakCache.Get(pakPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		entry, ok := idx.entries[key]
		if !ok {
			http.NotFound(w, r)
			return
		}

		f, err := os.Open(pakPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()

		st, err := f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		section := io.NewSectionReader(f, entry.Offset, entry.Size)
		http.ServeContent(w, r, path.Base(entry.Name), st.ModTime(), section)
	}
}
