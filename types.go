package samplingprofiler

// Frame 表示调用栈中的一个栈帧，按从根到叶子的顺序描述。
type Frame struct {
	// Function 函数或方法名（包含包/接收者限定）。
	Function string
	// File 源文件路径（可能为空）。
	File string
	// Line 源文件行号；未知时为 0。
	Line int
}

// Sample 是一条采样记录。
type Sample struct {
	// Source 执行单元标识（如 goroutine/线程/进程 ID）。
	Source string
	// TimestampNanos 采样时间戳（纳秒，单调或单调+墙钟均可）。
	// 约定同一 Source 的采样时间戳单调或乱序到达均可，重复定义为
	// (Source, TimestampNanos, 归一化栈指纹) 完全相同。
	TimestampNanos int64
	// Stack 调用栈，根帧在前、叶子帧在后。
	Stack []Frame
	// ValueNanos 该采样代表的时间长度（一个采样周期）。
	// <=0 时使用 Config.SamplePeriodNanos。每条唯一采样的权重固定，
	// 因而聚合结果只取决于“有哪些唯一采样”，与到达顺序无关。
	ValueNanos int64
}

// TruncationPolicy 控制超深调用栈的截断方式。
type TruncationPolicy int

const (
	// TruncationSentinel 保留最靠近根的 MaxDepth-1 个真实帧，
	// 再追加一个统一的 "<truncated>" 哨兵帧。
	TruncationSentinel TruncationPolicy = iota
	// TruncationTailDrop 直接丢弃超过 MaxDepth 的深层帧，
	// 不追加哨兵。
	TruncationTailDrop
)

// UnknownFramePolicy 控制信息缺失栈帧的处理方式。
type UnknownFramePolicy int

const (
	// UnknownKeep 原样保留未知帧。
	UnknownKeep UnknownFramePolicy = iota
	// UnknownDrop 丢弃未知帧。
	UnknownDrop
	// UnknownSentinel 替换为统一的 "<unknown>" 哨兵帧。
	UnknownSentinel
)

// RepeatedFramePolicy 控制相邻重复帧的处理方式。
type RepeatedFramePolicy int

const (
	// RepeatedKeep 保留相邻重复帧。
	RepeatedKeep RepeatedFramePolicy = iota
	// RepeatedCollapse 合并（折叠）相邻重复帧为一帧。
	RepeatedCollapse
)

// DownsamplePolicy 控制降采样策略。
type DownsamplePolicy int

const (
	// DownsampleNone 不降采样。
	DownsampleNone DownsamplePolicy = iota
	// DownsampleDeterministic 基于采样指纹做确定性哈希降采样：
	// 同一条采样在任何顺序/并发/重放下的取舍结果一致。
	DownsampleDeterministic
)

// Config 是聚合器的可调参数。零值不可直接使用，请用 DefaultConfig。
type Config struct {
	// MaxDepth 归一化栈最大深度；<=0 表示不限制。
	MaxDepth   int
	Truncation TruncationPolicy
	Unknown    UnknownFramePolicy
	Repeated   RepeatedFramePolicy
	Downsample DownsamplePolicy
	// DownsampleKeepNumerator / DownsampleKeepDenominator 定义保留比例
	// （保留概率 = 分子/分母）。
	DownsampleKeepNumerator   uint64
	DownsampleKeepDenominator uint64
	// MaxNodes 聚合树最大节点数；<=0 表示不限制。超限后的采样计入
	// Stats.NodesCapDropped，不会改动既有节点统计。
	MaxNodes int
	// DedupCapacityPerSource 每个执行单元去重指纹集合的容量（LRU）；
	// 容量足够时聚合结果与到达顺序无关。
	DedupCapacityPerSource int
	// MaxSources 执行单元数量上限；<=0 表示不限制。
	MaxSources int
	// SamplePeriodNanos 采样未显式给出 ValueNanos 时使用的周期长度。
	// <=0 时按 1 纳秒计。
	SamplePeriodNanos int64
}

// DefaultConfig 返回一份默认可用配置。
func DefaultConfig() Config {
	return Config{
		MaxDepth:                  128,
		Truncation:                TruncationSentinel,
		Unknown:                   UnknownKeep,
		Repeated:                  RepeatedKeep,
		Downsample:                DownsampleNone,
		DownsampleKeepNumerator:   0,
		DownsampleKeepDenominator: 0,
		MaxNodes:                  1_000_000,
		DedupCapacityPerSource:    100_000,
		MaxSources:                100_000,
		SamplePeriodNanos:         10_000_000,
	}
}

// Stats 是聚合过程的可核对计数。
type Stats struct {
	Received        int64 // 进入 Add 的原始采样数
	Duplicates      int64 // 命中去重被丢弃的采样数
	Downsampled     int64 // 被降采样丢弃的采样数
	UnknownFrames   int64 // 被处理（替换/丢弃）的未知帧数
	CollapsedFrames int64 // 被折叠的相邻重复帧数
	TruncatedStacks int64 // 触发截断的栈数
	EmptyStacks     int64 // 归一化后为空、被跳过的栈数
	NodesCapDropped int64 // 因节点上限未计入的采样数
	SourcesRejected int64 // 因执行单元数量上限被拒的采样数
	DedupEvictions  int64 // 去重集合发生淘汰的次数
	Accepted        int64 // 实际计入树的采样数
}

// NodeStats 是单个调用路径节点的统计。
type NodeStats struct {
	SelfNanos       int64 // 自身耗时：叶子命中该节点的时间之和
	CumulativeNanos int64 // 累计耗时：经过该节点的所有采样时间之和
	Samples         int64 // 命中该节点叶子的采样条数（自身口径）
}

// AggregatedNode 是查询结果中的一个节点。
type AggregatedNode struct {
	// Path 从根到该节点的归一化帧路径。
	Path []Frame
	// NodeKey 路径的稳定标识。
	NodeKey string
	Stats   NodeStats
}

// Snapshot 是某个自洽时刻的聚合视图。
type Snapshot struct {
	Nodes  []AggregatedNode // 按 NodeKey 字典序排列
	Config Config
	Stats  Stats
	// MinTimestampNanos / MaxTimestampNanos 为已计入采样的时间范围；
	// 无采样时 Max < Min。
	MinTimestampNanos int64
	MaxTimestampNanos int64
}

// errPersist 构造持久化相关的损坏错误（供内部包使用）。
func errPersist(reason string) error { return &CorruptionError{reason: reason} }
