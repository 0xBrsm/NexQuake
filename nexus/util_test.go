package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

func TestFNV64aHex_KnownVectors(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: "cbf29ce484222325"},
		{in: "hello", want: "a430d84680aabd0b"},
	}

	for _, tt := range tests {
		got := FNV64aHex(tt.in)
		if got != tt.want {
			t.Fatalf("FNV64aHex(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func writeTestPak(t *testing.T, pakPath string, files map[string][]byte) {
	t.Helper()

	type dirEnt struct {
		name   string
		offset int32
		size   int32
	}

	var data bytes.Buffer
	data.Grow(12)
	data.Write(make([]byte, 12)) // header placeholder

	entries := make([]dirEnt, 0, len(files))
	for name, content := range files {
		off := int32(data.Len())
		data.Write(content)
		entries = append(entries, dirEnt{name: name, offset: off, size: int32(len(content))})
	}

	dirOffset := int32(data.Len())
	for _, e := range entries {
		nameBytes := make([]byte, 56)
		copy(nameBytes, []byte(e.name))
		data.Write(nameBytes)

		var tmp [8]byte
		binary.LittleEndian.PutUint32(tmp[0:4], uint32(e.offset))
		binary.LittleEndian.PutUint32(tmp[4:8], uint32(e.size))
		data.Write(tmp[:])
	}
	dirLen := int32(len(entries) * 64)

	out := data.Bytes()
	copy(out[0:4], []byte("PACK"))
	binary.LittleEndian.PutUint32(out[4:8], uint32(dirOffset))
	binary.LittleEndian.PutUint32(out[8:12], uint32(dirLen))

	if err := os.WriteFile(pakPath, out, 0o644); err != nil {
		t.Fatalf("write pak: %v", err)
	}
}
