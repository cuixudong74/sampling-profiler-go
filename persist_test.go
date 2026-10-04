package profiler

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Recursion = RecursionFold
	cfg.MaxDepth = 5
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.Ingest(
		sample("A", 1, "leaf", "leaf", "root"),
		sample("A", 2, "leaf", "root"),
		sample("B", 1, "leaf", "mid", "root"),
	)
	want := a.Commit()

	path := filepath.Join(t.TempDir(), "profile.bin")
	if err := Save(want, path); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshotsEqual(t, want, got)
	if !reflect.DeepEqual(got.Config(), want.Config()) {
		t.Fatalf("config mismatch: %+v vs %+v", got.Config(), want.Config())
	}
	if got.Stats() != want.Stats() {
		t.Fatalf("stats mismatch: %+v vs %+v", got.Stats(), want.Stats())
	}
}

func TestCorruptionAndBackup(t *testing.T) {
	a, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	a.Ingest(sample("A", 1, "leaf", "root"))
	first := a.Commit()

	path := filepath.Join(t.TempDir(), "profile.bin")
	if err := Save(first, path); err != nil {
		t.Fatal(err)
	}
	a.Ingest(sample("A", 2, "leaf", "root"))
	second := a.Commit()
	if err := Save(second, path); err != nil {
		t.Fatal(err)
	}

	// Corrupt the main file: checksum verification must catch it.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if !errors.Is(err, ErrLoadedFromBackup) {
		t.Fatalf("expected ErrLoadedFromBackup, got %v", err)
	}
	if got == nil {
		t.Fatal("expected backup snapshot")
	}
	if got.Total() != first.Total() {
		t.Fatalf("backup should be the first version: total %d vs %d", got.Total(), first.Total())
	}

	// Corrupt the backup too: hard error.
	bak := path + ".bak"
	bakData, err := os.ReadFile(bak)
	if err != nil {
		t.Fatal(err)
	}
	bakData[len(bakData)-1] ^= 0xff
	if err := os.WriteFile(bak, bakData, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt, got %v", err)
	}
}

func TestBadMagic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.bin")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 128), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrBadMagic) {
		t.Fatalf("expected ErrBadMagic, got %v", err)
	}
}

func TestNoTempFilesLeft(t *testing.T) {
	a, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	a.Ingest(sample("A", 1, "leaf", "root"))
	snap := a.Commit()
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.bin")
	if err := Save(snap, path); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".bin" && e.Name() != "profile.bin.bak" {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}
