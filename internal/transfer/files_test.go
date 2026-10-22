package transfer

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTargetTraversalSymlinkAndOverwrite(t *testing.T) {
	root := t.TempDir()
	entry := []Entry{{Name: "file.txt", Size: 4}}
	for _, target := range []string{"../escape", filepath.Join(root, "..", "outside")} {
		if _, err := ProbeTarget(root, target, entry, false, 0); err == nil {
			t.Fatal("traversal accepted")
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err == nil {
		if _, err = ProbeTarget(root, "link", entry, false, 0); err == nil {
			t.Fatal("symlink accepted")
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ProbeTarget(root, ".", entry, false, 0); err == nil {
		t.Fatal("overwrite accepted")
	}
	if _, err := ProbeTarget(root, ".", entry, true, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "file.txt"))
	if string(got) != "keep" {
		t.Fatal("probe changed file")
	}
	for _, name := range []string{"../x", "/abs", "a/../../b", "a\\b", "C:/x"} {
		if err := ValidateEntries([]Entry{{Name: name}}); err == nil {
			t.Fatalf("invalid entry accepted: %s", name)
		}
	}
}
func TestExtractRejectsUnexpectedArchiveEntries(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "malicious.tar"))
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	_ = tw.WriteHeader(&tar.Header{Name: "../escape", Size: 1, Mode: 0644})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	_ = f.Close()
	root := t.TempDir()
	if err = Extract(context.Background(), f.Name(), root, ".", []Entry{{Name: "safe", Size: 1}}, false); err == nil {
		t.Fatal("archive traversal accepted")
	}
}
