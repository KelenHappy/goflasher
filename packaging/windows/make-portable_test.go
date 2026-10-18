package main

import (
	"os"
	"path/filepath"
	"testing"
)

// wimlibFixture writes an executable, with or without the wimlib marker, and
// a complete compliance record, and returns the layout that stages them.
func wimlibFixture(t *testing.T, linked bool) portableLayout {
	t.Helper()
	dir := t.TempDir()
	exe := []byte("MZ raw writer")
	if linked {
		exe = append(exe, wimlibMarker...)
	}
	l := portableLayout{
		executable:   filepath.Join(dir, "GoFlasher.exe"),
		stageDir:     filepath.Join(dir, "stage"),
		wimlibRecord: filepath.Join(dir, "record"),
	}
	writeFile(t, l.executable, string(exe))
	for _, item := range wimlibRecordItems {
		writeFile(t, filepath.Join(l.wimlibRecord, item), item)
	}
	writeFile(t, filepath.Join(l.wimlibRecord, "licenses", "COPYING"), "COPYING")
	return l
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestStageWimlibRecordShipsRecordForLinkedExecutable(t *testing.T) {
	l := wimlibFixture(t, true)
	if err := l.stageWimlibRecord(); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(l.stageDir, "legal", "wimlib-1.14.5")
	for _, item := range append(wimlibRecordItems, filepath.Join("licenses", "COPYING")) {
		if _, err := os.Stat(filepath.Join(dest, item)); err != nil {
			t.Errorf("staged record lacks %s: %v", item, err)
		}
	}
}

func TestStageWimlibRecordRequiresRecordForLinkedExecutable(t *testing.T) {
	l := wimlibFixture(t, true)
	l.wimlibRecord = ""
	if err := l.stageWimlibRecord(); err == nil {
		t.Fatal("packaged a wimlib-linked executable without its record")
	}
}

func TestStageWimlibRecordSkipsRawWriterExecutable(t *testing.T) {
	l := wimlibFixture(t, false)
	if err := l.stageWimlibRecord(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.stageDir, "legal")); !os.IsNotExist(err) {
		t.Fatalf("raw-writer ZIP gained a wimlib record: %v", err)
	}
}
