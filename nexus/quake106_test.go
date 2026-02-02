package main

import (
	"archive/zip"
	"path/filepath"
	"testing"
)

func TestQuake106Extract(t *testing.T) {
	zr, err := zip.OpenReader(filepath.Join("..", "assets", "quake106.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if err := extractCanonicalQuake106Pak0(&zr.Reader, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
