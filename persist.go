package profiler

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Persistence errors.
var (
	// ErrCorrupt is returned when a profile file fails integrity verification.
	ErrCorrupt = errors.New("profiler: corrupt profile file")
	// ErrBadMagic is returned when the file is not a profile file.
	ErrBadMagic = errors.New("profiler: unrecognized profile file format")
	// ErrLoadedFromBackup is returned as a warning when the main file was
	// corrupt and the backup copy was loaded instead.
	ErrLoadedFromBackup = errors.New("profiler: loaded from backup after corruption")
)

const (
	persistMagic   = "SPROF1\x00\x00"
	persistVersion = uint32(1)
	checksumSize   = sha256.Size
)

type persistPayload struct {
	Version uint32
	Nodes   []persistNode
	Total   int64
	Stats   Stats
	Config  Config
}

type persistNode struct {
	Path []Frame
	Self int64
	Cum  int64
}

// Save writes the snapshot to path atomically: data goes to a temp file in
// the same directory, is fsynced, and only then renamed over the target. An
// interrupted write therefore leaves either the previous file or a leftover
// temp file, never a half-written target. The previous file is kept as
// <path>.bak.
func Save(s *Snapshot, path string) error {
	payload, err := encodeSnapshot(s)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".profiler-*")
	if err != nil {
		return fmt.Errorf("profiler: create temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write([]byte(persistMagic)); err != nil {
		tmp.Close()
		return fmt.Errorf("profiler: write magic: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return fmt.Errorf("profiler: write payload: %w", err)
	}
	sum := sha256.Sum256(payload)
	if _, err := tmp.Write(sum[:]); err != nil {
		tmp.Close()
		return fmt.Errorf("profiler: write checksum: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("profiler: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("profiler: close: %w", err)
	}

	// Keep the previous version as a backup before the atomic rename.
	if _, err := os.Stat(path); err == nil {
		_ = os.Remove(path + ".bak")
		if err := os.Rename(path, path+".bak"); err != nil {
			return fmt.Errorf("profiler: backup: %w", err)
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("profiler: rename: %w", err)
	}
	cleanup = false
	return nil
}

// Load reads a snapshot previously written by Save. It verifies the magic and
// a SHA-256 checksum; a corrupt file yields ErrCorrupt. If the main file is
// corrupt but a <path>.bak backup exists, the backup is loaded and
// ErrLoadedFromBackup is returned alongside the snapshot.
func Load(path string) (*Snapshot, error) {
	s, err := loadFile(path)
	if err == nil {
		return s, nil
	}
	if errors.Is(err, ErrCorrupt) || errors.Is(err, ErrBadMagic) {
		if bak, bakErr := loadFile(path + ".bak"); bakErr == nil {
			return bak, ErrLoadedFromBackup
		}
	}
	return nil, err
}

func loadFile(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < len(persistMagic)+checksumSize {
		return nil, fmt.Errorf("%w: file too short", ErrCorrupt)
	}
	if string(data[:len(persistMagic)]) != persistMagic {
		return nil, ErrBadMagic
	}
	payload := data[len(persistMagic) : len(data)-checksumSize]
	got := data[len(data)-checksumSize:]
	want := sha256.Sum256(payload)
	if !bytes.Equal(got, want[:]) {
		return nil, fmt.Errorf("%w: checksum mismatch", ErrCorrupt)
	}

	var p persistPayload
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(&p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if p.Version != persistVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrCorrupt, p.Version)
	}

	nodes := make(map[string]*Node, len(p.Nodes))
	for _, pn := range p.Nodes {
		n := &Node{
			Path: append([]Frame(nil), pn.Path...),
			Self: pn.Self,
			Cum:  pn.Cum,
		}
		nodes[PathKey(n.Path)] = n
	}
	return &Snapshot{nodes: nodes, total: p.Total, stats: p.Stats, config: p.Config}, nil
}

func encodeSnapshot(s *Snapshot) ([]byte, error) {
	p := persistPayload{
		Version: persistVersion,
		Total:   s.total,
		Stats:   s.stats,
		Config:  s.config,
		Nodes:   make([]persistNode, 0, len(s.nodes)),
	}
	for _, n := range s.nodes {
		p.Nodes = append(p.Nodes, persistNode{
			Path: append([]Frame(nil), n.Path...),
			Self: n.Self,
			Cum:  n.Cum,
		})
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&p); err != nil {
		return nil, fmt.Errorf("profiler: encode: %w", err)
	}
	return buf.Bytes(), nil
}
