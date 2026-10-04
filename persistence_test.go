package samplingprofiler

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func populatedAggregator(t *testing.T) *Aggregator {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DedupCapacityPerSource = 64
	a := NewAggregator(cfg)
	samples := []Sample{
		{Source: "u1", TimestampNanos: 10, ValueNanos: 10, Stack: mkStack("main", "serve", "handle")},
		{Source: "u1", TimestampNanos: 20, ValueNanos: 10, Stack: mkStack("main", "serve", "query")},
		{Source: "u2", TimestampNanos: 15, ValueNanos: 10, Stack: mkStack("main", "bg", "gc")},
	}
	// 含重复，验证去重状态也被持久化。
	samples = append(samples, samples[0])
	for _, s := range samples {
		a.Add(s)
	}
	return a
}

func TestSaveLoadRoundTrip(t *testing.T) {
	a := populatedAggregator(t)
	before := a.Snapshot()

	var buf bytes.Buffer
	if err := a.Save(&buf); err != nil {
		t.Fatal(err)
	}
	b, err := Load(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	after := b.Snapshot()
	if !reflect.DeepEqual(before.Nodes, after.Nodes) {
		t.Fatal("nodes differ after reload")
	}
	if before.Stats != after.Stats {
		t.Fatalf("stats differ: %+v vs %+v", before.Stats, after.Stats)
	}
	if before.MinTimestampNanos != after.MinTimestampNanos || before.MaxTimestampNanos != after.MaxTimestampNanos {
		t.Fatal("time range differs after reload")
	}

	// 重启后重放同一批采样：重复必须仍被识别，统计保持一致。
	for _, s := range []Sample{
		{Source: "u1", TimestampNanos: 10, ValueNanos: 10, Stack: mkStack("main", "serve", "handle")},
	} {
		b.Add(s)
	}
	if got := b.Stats(); got.Accepted != before.Stats.Accepted {
		t.Fatalf("replayed duplicate after restart was counted: %+v", got)
	}
}

func TestSaveLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "profile.bin")
	a := populatedAggregator(t)
	if err := a.SaveToFile(path); err != nil {
		t.Fatal(err)
	}
	b, err := LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Snapshot().Nodes, b.Snapshot().Nodes) {
		t.Fatal("file round-trip mismatch")
	}
	// 目标目录不得残留临时文件。
	fis, _ := os.ReadDir(filepath.Dir(path))
	for _, fi := range fis {
		if strings.HasPrefix(fi.Name(), ".sprof-") {
			t.Fatalf("temp file left behind: %s", fi.Name())
		}
	}
}

func TestCorruptionDetected(t *testing.T) {
	a := populatedAggregator(t)
	var good bytes.Buffer
	if err := a.Save(&good); err != nil {
		t.Fatal(err)
	}
	raw := good.Bytes()

	cases := map[string][]byte{
		"empty":        nil,
		"truncated":    raw[:len(raw)/2],
		"bad magic":    append([]byte("XXXXXX"), raw[6:]...),
		"flipped byte": append(append([]byte(nil), raw[:len(raw)-1]...), raw[len(raw)-1]^0xff),
		"garbage tail": append(append([]byte(nil), raw...), 0x00),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(bytes.NewReader(data))
			var ce *CorruptionError
			if err == nil || !errors.As(err, &ce) {
				t.Fatalf("expected CorruptionError, got %v", err)
			}
		})
	}
}

func TestAtomicWriteKeepsOldFileOnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.bin")
	a := populatedAggregator(t)
	if err := a.SaveToFile(path); err != nil {
		t.Fatal(err)
	}
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// 第二次保存：原子替换成功后，目录中不得残留临时文件。
	a2 := NewAggregator(DefaultConfig())
	if err := a2.SaveToFile(path); err != nil {
		t.Fatal(err)
	}
	for _, fi := range mustReadDir(t, dir) {
		if strings.HasPrefix(fi.Name(), ".sprof-") {
			t.Fatalf("temp file left behind: %s", fi.Name())
		}
	}

	// 当前磁盘内容（空聚合器）。
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 目标路径本身是一个目录时 rename 必然失败；文件必须保持不变，
	// 且父目录不能残留临时文件。
	dirTarget := filepath.Join(dir, "isdir")
	if err := os.Mkdir(dirTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveToFile(dirTarget); err == nil {
		t.Fatal("expected failure renaming temp file over a directory")
	}
	// 父目录无临时文件残留。
	for _, fi := range mustReadDir(t, dir) {
		if strings.HasPrefix(fi.Name(), ".sprof-") {
			t.Fatalf("temp file leaked: %s", fi.Name())
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, cur) {
		t.Fatal("file must stay intact after a failed write")
	}
	if b, err := LoadFromFile(path); err != nil || len(b.Snapshot().Nodes) != 0 {
		t.Fatalf("current file must remain loadable and unchanged: err=%v", err)
	}
	_ = orig
}

func mustReadDir(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
