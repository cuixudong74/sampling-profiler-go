package samplingprofiler

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
)

// 文件格式（小端序）：
//
//	magic      [6]byte  "SPROF"
//	version    uint16   当前为 1
//	kind       uint16   1 = snapshot+dedup 状态
//	payloadLen uint64   JSON 负载字节数
//	crc32      uint32   对“头部中 crc 字段之后 + 负载”的 IEEE CRC-32
//	payload    JSON（长度 = payloadLen）
//
// 写入采用“临时文件 -> fsync -> rename”原子替换；加载时校验魔数、
// 版本、长度与 CRC，并完整反序列化。任何一步失败都返回
// *CorruptionError，调用方可据此选择丢弃旧文件重新聚合。

const (
	persistMagic   = "SPROF1" // 恰好 6 字节
	persistVersion = uint16(1)
	persistKind    = uint16(1)
	headerLen      = 6 + 2 + 2 + 8 + 4 // 22
)

// persistedNode 是节点的序列化表示。
type persistedNode struct {
	Depth   int    `json:"d"`
	Frame   Frame  `json:"f"`
	Key     string `json:"k"`
	Parent  string `json:"p"`
	Self    int64  `json:"s"`
	Cum     int64  `json:"c"`
	Samples int64  `json:"n"`
}

// persistedPayload 是落盘的完整逻辑状态。
type persistedPayload struct {
	Config Config                   `json:"config"`
	Stats  Stats                    `json:"stats"`
	MinTS  int64                    `json:"minTS"`
	MaxTS  int64                    `json:"maxTS"`
	Nodes  []persistedNode          `json:"nodes"`
	Dedup  map[string]exportedDedup `json:"dedup"`
}

// CorruptionError 表示持久化文件损坏或被中断。
type CorruptionError struct{ reason string }

func (e *CorruptionError) Error() string {
	return "profile persistence corrupt: " + e.reason
}

func buildHeader(payloadLen int, checksum uint32) []byte {
	buf := make([]byte, headerLen)
	copy(buf[0:6], persistMagic)
	binary.LittleEndian.PutUint16(buf[6:8], persistVersion)
	binary.LittleEndian.PutUint16(buf[8:10], persistKind)
	binary.LittleEndian.PutUint64(buf[10:18], uint64(payloadLen))
	binary.LittleEndian.PutUint32(buf[18:22], checksum)
	return buf
}

// Save 将聚合状态原子写入 w。
func (a *Aggregator) Save(w io.Writer) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	payload := persistedPayload{
		Config: a.cfg,
		Stats:  a.stats,
		MinTS:  a.minTS,
		MaxTS:  a.maxTS,
		Dedup:  a.dedup.export(),
	}
	for _, an := range a.tree.snapshot() {
		n := a.tree.nodes[an.NodeKey]
		pn := persistedNode{
			Depth:   n.depth,
			Frame:   n.frame,
			Key:     n.key,
			Self:    n.self,
			Cum:     n.cum,
			Samples: n.samples,
		}
		if n.parent != nil && n.parent != a.tree.root {
			pn.Parent = n.parent.key
		}
		payload.Nodes = append(payload.Nodes, pn)
	}
	body, err := json.Marshal(&payload)
	if err != nil {
		return fmt.Errorf("save profile: %w", err)
	}

	crc := crc32.NewIEEE()
	hdrNoCRC := make([]byte, headerLen-4)
	copy(hdrNoCRC[0:6], persistMagic)
	binary.LittleEndian.PutUint16(hdrNoCRC[6:8], persistVersion)
	binary.LittleEndian.PutUint16(hdrNoCRC[8:10], persistKind)
	binary.LittleEndian.PutUint64(hdrNoCRC[10:18], uint64(len(body)))
	crc.Write(hdrNoCRC)
	crc.Write(body)

	if _, err := w.Write(buildHeader(len(body), crc.Sum32())); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// Load 从 r 加载，返回与落盘时等价的聚合器。
func Load(r io.Reader) (*Aggregator, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(data) < headerLen {
		return nil, &CorruptionError{"file truncated before header"}
	}
	if string(data[0:6]) != persistMagic {
		return nil, &CorruptionError{"bad magic"}
	}
	version := binary.LittleEndian.Uint16(data[6:8])
	if version != persistVersion {
		return nil, &CorruptionError{fmt.Sprintf("unsupported version %d", version)}
	}
	kind := binary.LittleEndian.Uint16(data[8:10])
	if kind != persistKind {
		return nil, &CorruptionError{fmt.Sprintf("unsupported kind %d", kind)}
	}
	plen := int(binary.LittleEndian.Uint64(data[10:18]))
	storedCRC := binary.LittleEndian.Uint32(data[18:22])
	if headerLen+plen != len(data) {
		return nil, &CorruptionError{"declared payload length does not match file size"}
	}
	body := data[headerLen:]
	crc := crc32.NewIEEE()
	crc.Write(data[:headerLen-4])
	crc.Write(body)
	if crc.Sum32() != storedCRC {
		return nil, &CorruptionError{"checksum mismatch"}
	}

	var p persistedPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, &CorruptionError{"payload unmarshal: " + err.Error()}
	}

	tree, err := restoreTree(p.Nodes)
	if err != nil {
		return nil, err
	}
	dedup, err := restoreDedupStates(p.Config.DedupCapacityPerSource, p.Config.MaxSources, p.Dedup)
	if err != nil {
		return nil, err
	}
	a := &Aggregator{
		cfg:   p.Config,
		tree:  tree,
		dedup: dedup,
		stats: p.Stats,
		minTS: p.MinTS,
		maxTS: p.MaxTS,
	}
	return a, nil
}

// restoreTree 从节点列表重建聚合树并校验父子关系的一致性。
func restoreTree(nodes []persistedNode) (*profileTree, error) {
	t := newProfileTree()
	byKey := make(map[string]*persistedNode, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		if _, dup := byKey[n.Key]; dup {
			return nil, &CorruptionError{"duplicate node key in payload"}
		}
		byKey[n.Key] = n
	}
	// 按深度升序插入，保证父先于子。
	inserted := make(map[string]*profileNode, len(nodes))
	remaining := make([]*persistedNode, 0, len(nodes))
	for _, n := range byKey {
		remaining = append(remaining, n)
	}
	// 稳定排序，保证报错与结果可复现。
	sortPersisted(remaining)
	for len(remaining) > 0 {
		progress := false
		next := remaining[:0]
		for _, n := range remaining {
			if n.Depth < 1 {
				return nil, &CorruptionError{"node depth must be >= 1"}
			}
			var parent *profileNode
			if n.Parent == "" {
				if n.Depth != 1 {
					return nil, &CorruptionError{"root-level node has depth != 1"}
				}
				parent = t.root
			} else {
				p, ok := inserted[n.Parent]
				if !ok {
					// 父尚未插入，留待下一轮。
					if byKey[n.Parent] == nil {
						return nil, &CorruptionError{"missing parent: " + n.Parent}
					}
					next = append(next, n)
					continue
				}
				if p.depth+1 != n.Depth {
					return nil, &CorruptionError{"depth inconsistent with parent"}
				}
				parent = p
			}
			pn := &profileNode{
				frame:    n.Frame,
				key:      n.Key,
				parent:   parent,
				depth:    n.Depth,
				children: make(map[string]*profileNode),
				self:     n.Self,
				cum:      n.Cum,
				samples:  n.Samples,
			}
			fk := frameKey(n.Frame)
			if _, exists := parent.children[fk]; exists {
				return nil, &CorruptionError{"sibling frame key collision"}
			}
			parent.children[fk] = pn
			t.nodes[pn.key] = pn
			inserted[pn.key] = pn
			t.nodeCount++
			progress = true
		}
		remaining = next
		if !progress {
			return nil, &CorruptionError{"cycle or unresolved parent references"}
		}
	}
	return t, nil
}

// SaveToFile 原子写入：写临时文件、fsync 后 rename 替换目标。
func (a *Aggregator) SaveToFile(path string) (err error) {
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".sprof-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// 任何失败路径都清理临时文件（已 Close 时 Remove 仍有效）。
	completed := false
	defer func() {
		if !completed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	var buf bytes.Buffer
	if err = a.Save(&buf); err != nil {
		return err
	}
	if _, err = tmp.Write(buf.Bytes()); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpName, path); err != nil {
		return err
	}
	completed = true
	return nil
}

// LoadFromFile 从文件加载；文件不存在时返回 os 包的原始“不存在”错误。
func LoadFromFile(path string) (*Aggregator, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Load(f)
}

func sortPersisted(ns []*persistedNode) {
	// 简单插入排序：节点数通常远小于输入采样数；并按 key 保证稳定。
	for i := 1; i < len(ns); i++ {
		for j := i; j > 0; j-- {
			a, b := ns[j-1], ns[j]
			if a.Depth < b.Depth || (a.Depth == b.Depth && a.Key <= b.Key) {
				break
			}
			ns[j-1], ns[j] = b, a
		}
	}
}
