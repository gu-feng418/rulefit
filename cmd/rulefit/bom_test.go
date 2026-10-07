package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTextFilesAreCleanUTF8 guards two encoding traps that each cost this
// repository a debugging round: a UTF-8 byte-order mark, which encoding/json and
// gofmt both reject, and the U+FFFD replacement character, which is what a
// UTF-8 file looks like after a tool rewrites it through a legacy code page. Both
// are invisible in a diff and neither shows up in a compile error.
func TestTextFilesAreCleanUTF8(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bom := []byte{0xEF, 0xBB, 0xBF}
	replacement := []byte("\uFFFD")
	checked := 0

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".go", ".json", ".md", ".yml", ".yaml", ".mod", ".txt":
		default:
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checked++
		rel, _ := filepath.Rel(root, path)

		if bytes.HasPrefix(data, bom) {
			t.Errorf("%s starts with a UTF-8 BOM; encoding/json and gofmt reject it", rel)
		}
		if !utf8.Valid(data) {
			t.Errorf("%s is not valid UTF-8", rel)
		}
		if bytes.Contains(data, replacement) {
			t.Errorf("%s contains U+FFFD, which is how a UTF-8 file looks after being rewritten through a legacy code page", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no files were checked; the walk is broken")
	}
}
