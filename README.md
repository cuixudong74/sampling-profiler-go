# sampling-profiler-go

进程内的采样剖析数据聚合与对比库。无外部服务依赖、无第三方库依赖，
仅使用 Go 标准库；聚合结果只取决于“有哪些唯一采样”，与采样的
到达顺序、批次划分、并发度无关，可在本地稳定复现。

## 快速开始

```go
cfg := samplingprofiler.DefaultConfig()
agg := samplingprofiler.NewAggregator(cfg)

// 多来源、多 goroutine 并发上报；乱序、重复、迟到都允许。
agg.Add(samplingprofiler.Sample{
    Source:         "worker-7",
    TimestampNanos: 123456789,
    Stack: []samplingprofiler.Frame{
        {Function: "main.main", File: "main.go", Line: 21},
        {Function: "main.serve", File: "server.go", Line: 88},
        {Function: "main.handle", File: "handler.go", Line: 14},
    },
})

snap := agg.Snapshot()          // 自洽快照
for _, n := range snap.Nodes { // 按 NodeKey 字典序
    fmt.Println(n.NodeKey, n.Stats.SelfNanos, n.Stats.CumulativeNanos)
}
```

版本对比：

```go
diff := samplingprofiler.Compare(v1Snapshot, v2Snapshot, samplingprofiler.CompareConfig{
    Policy: samplingprofiler.MatchLeaf,
    Renames: []samplingprofiler.RenameRule{
        {Left: oldNodeKey, Right: newNodeKey},
    },
})
```

持久化：

```go
_ = agg.SaveToFile("data/profile.bin")           // 临时文件 + fsync + rename
restored, err := samplingprofiler.LoadFromFile("data/profile.bin")
// 损坏/写中断会返回 *samplingprofiler.CorruptionError
```

## 数据模型

- `Frame{Function, File, Line}`：栈帧。`Function` 为空表示未知帧。
- `Sample{Source, TimestampNanos, Stack, ValueNanos}`：
  - `Source`：执行单元标识（goroutine / 线程 / 进程）；
  - `TimestampNanos`：采样时刻；
  - `Stack`：根帧在前、叶子帧在后；
  - `ValueNanos`：该采样代表的时间长度（一个采样周期）；`<=0`
    时使用 `Config.SamplePeriodNanos`（默认 10ms；仍 `<=0` 时按 1）。

时间口径采用“每条唯一采样代表一个固定时长的采样周期”的标准采样
模型（而不是用相邻采样时间差推导区间），因此迟到、乱序不会改变
任何其他采样的权重——这是顺序无关性的基础。

## 聚合语义

对每条**被接受**的采样（权重 `ValueNanos`）沿归一化后的栈路径：

- 路径上每个节点：`CumulativeNanos += ValueNanos`；
- 仅叶子节点：`SelfNanos += ValueNanos`、`Samples++`。

因此恒有 `CumulativeNanos >= SelfNanos`。节点由从根开始的完整
归一化路径标识（`NodeKey`），相同路径在任意来源/顺序下合并到同一
节点。快照中的节点按 `NodeKey` 字典序排列。

### 一条采样的处理管线

全部在聚合器互斥锁内完成，对外原子可见（查询不会看到半累计）：

1. `Received++`；
2. 栈归一化（见下）；归一化后为空计入 `EmptyStacks` 并丢弃；
3. 确定性降采样（见下），丢弃计入 `Downsampled`；
4. 去重（见下）：重复计入 `Duplicates`，来源超限计入
   `SourcesRejected`，LRU 淘汰计入 `DedupEvictions`；
5. 节点上限检查：需要新建节点会使节点数超过 `MaxNodes` 时，
   **整条采样丢弃**（`NodesCapDropped++`），本次写入完全不生效，
   既有节点与统计不变；
6. 计入聚合树并刷新时间范围，`Accepted++`。

恒等式可用于核对：

```
Received = Accepted + Duplicates + Downsampled + EmptyStacks
         + NodesCapDropped + SourcesRejected
```

### 去重（乱序、重复、迟到）

判重在每个执行单元内独立进行，满足任一条件即丢弃：

1. `(Source, TimestampNanos, 归一化栈指纹)` 三元组完全相同
   ——同一条采样被重复上报；
2. `(Source, TimestampNanos)` 已出现但栈不同
   ——该执行单元在该时刻的时间已经累计过，不得重复累计。

仅栈相同但时间戳不同属于不同周期的正常采样，照常计入；迟到采样
（时间戳早于已见最小值）不被丢弃，时间范围会相应回扩。

每个执行单元维护两个等容量的有界 LRU 集合（三元组复合键集合、
时间戳集合，哈希均为 FNV-1a）。当
`DedupCapacityPerSource` **不小于该单元不同采样条数**时，去重
结果与到达顺序、批次划分、并发度完全无关；容量不足时淘汰最久未
使用的标识并计入 `DedupEvictions`，此后被淘汰标识的再次到达无法
识别——这是“内存不随输入总量无界增长”的既定代价。若要求在无限
输入下严格去重，应按时间轮转聚合器（例如按窗口新建聚合器），而
不是调大 LRU。执行单元数量受 `MaxSources` 限制。

### 栈归一化（顺序固定，全部确定性）

1. **未知帧**（`Function == ""`），由 `Unknown` 决定：
   - `UnknownKeep`（默认）：原样保留；
   - `UnknownDrop`：丢弃该帧（计数 `UnknownFrames`）；
   - `UnknownSentinel`：替换为统一的 `<unknown>` 帧。
2. **相邻重复帧**，由 `Repeated` 决定：
   - `RepeatedKeep`（默认）：保留；
   - `RepeatedCollapse`：相邻且 `frameKey` 完全相同的帧折叠为一帧
     （计数 `CollapsedFrames`）。
3. **超深截断**，当深度超过 `MaxDepth` 时（保留靠近根的前缀）：
   - `TruncationSentinel`（默认）：保留前 `MaxDepth-1` 个真实帧，
     再追加统一的 `<truncated>` 哨兵帧；
   - `TruncationTailDrop`：直接丢弃超过 `MaxDepth` 的深层帧。

哨兵帧全局统一，因此不同来源的同类被截断/未知栈合并到同一节点。
所有策略只修改被处理的帧本身，不改变其余帧的 `NodeKey` 与统计。

`frameKey` 使用长度前缀编码 `(Function, File, Line)`，不存在分隔符
歧义；`NodeKey` 以记录分隔符（U+001E）连接各帧的 `frameKey`。

### 降采样与内存有界

- `DownsampleNone`（默认）：不丢弃。
- `DownsampleDeterministic`：对
  `(Source, TimestampNanos, 栈指纹)` 做 FNV-1a 哈希后取模，
  `h mod Denominator < Numerator` 时保留。决策输入与到达顺序无关，
  同一条采样在任何顺序、并发、批次乃至跨进程重放下取舍一致，
  被丢弃数量由 `Downsampled` 核对。
- `MaxNodes`：聚合树节点硬上限，内存占用为
  `O(节点数 + 来源数 × 去重容量)`，不随输入总量无界增长。

## 并发模型

`Aggregator` 内部用一把 `sync.RWMutex` 保护全部可变状态：

- 任意数量的来源 goroutine 可并发 `Add`；
- `Snapshot` / `Stats` 在读锁下返回**深拷贝**（节点、路径均为新
  分配切片），查询看到的永远是某个自洽的完整状态，不会读到只累计
  一半的树；
- 单把互斥锁使“判重 + 插入”天然原子（并发重复上报恰好一次计入），
  也使内存有界策略（LRU、节点上限）无需额外同步。

该实现面向正确性与可解释性；单锁在极高写入吞吐下会成为串行点，
但避免了分片方案引入的顺序相关合并问题。

## 版本对比与节点匹配

`Compare(left, right, cc)` 输出 `Matched`（含 self/cum 的左右值与
差值）、`OnlyLeft`、`OnlyRight`，全部按 `NodeKey` 排序。匹配按
以下顺序对未匹配节点逐级尝试（由 `CompareConfig.Policy` 决定启用
到哪一级）：

1. `exact`：两版本完整归一化路径 `NodeKey` 相同；
2. `rename`：命中显式 `RenameRule{Left, Right}`（两边均未匹配时
   生效）。用于函数重命名；
3. `leaf`：最终帧 `frameKey`（函数名+文件+行）在双方未匹配集合中
   **都恰好唯一**时匹配，用于“函数身份不变、调用位置移动”。任何
   一方为 0 个或多于 1 个候选都**不匹配**，节点保留在
   `OnlyLeft/OnlyRight`（宁可不确定不错配）。

策略枚举：`MatchExactPath`（仅 1）、`MatchRenames`（1+2）、
`MatchLeaf`（1+2+3）。每个匹配结果以 `MatchedBy` 标明命中方式。

## 持久化格式

文件为自描述二进制头 + JSON 负载：

```
magic(6)="SPROF1" | version(u16=1) | kind(u16=1) | payloadLen(u64)
| crc32(u32) | JSON payload
```

- CRC-32 (IEEE) 覆盖“头部魔数到 payloadLen 部分”与整个负载；
- 加载时校验魔数、版本、类型、声明长度与 CRC，并完整重建/校验树
  的父子关系（深度一致、无悬挂父、无环、无同级帧冲突）。任何一步
  失败返回 `*CorruptionError`，错误信息说明原因；
- `SaveToFile` 采用“同目录临时文件 → `fsync` → `rename`”原子
  替换（失败路径清理临时文件），写中断不会破坏既有文件；
- 持久化内容包含配置、统计计数、时间范围、聚合树与去重状态
  （LRU 按最近到最久顺序保存，恢复后淘汰行为与连续运行一致）。
  重启后重放同一批采样，聚合结果与未重启时一致，重复仍被识别。

建议的损坏处理策略：加载方对 `*CorruptionError` 选择“删除旧文件 +
从空状态重新聚合”（采样来源若可回放则重放）。

## 可调参数一览（`Config`，默认值见 `DefaultConfig`）

| 字段 | 默认 | 含义 |
| --- | --- | --- |
| `MaxDepth` | 128 | 归一化栈最大深度；`<=0` 不限制 |
| `Truncation` | `TruncationSentinel` | 超深栈截断策略 |
| `Unknown` | `UnknownKeep` | 未知帧处理策略 |
| `Repeated` | `RepeatedKeep` | 相邻重复帧处理策略 |
| `Downsample` | `DownsampleNone` | 降采样策略 |
| `DownsampleKeepNumerator` / `...Denominator` | 0 / 0（=全保留） | 确定性降采样保留比例 |
| `MaxNodes` | 1,000,000 | 聚合树节点上限；`<=0` 不限制 |
| `DedupCapacityPerSource` | 100,000 | 每执行单元去重 LRU 容量 |
| `MaxSources` | 100,000 | 执行单元数量上限；`<=0` 不限制 |
| `SamplePeriodNanos` | 10,000,000 | 采样未给 `ValueNanos` 时的权重 |

`Stats` 字段：`Received, Accepted, Duplicates, Downsampled,
UnknownFrames, CollapsedFrames, TruncatedStacks, EmptyStacks,
NodesCapDropped, SourcesRejected, DedupEvictions`。

## 测试

```
go test -race ./...   # 含竞态检测
```

覆盖：乱序/重复/迟到的顺序无关性与计数核对、不同批次划分等价性、
自身/累计口径、深栈与未知帧策略、确定性降采样、版本对比（精确 /
重命名 / 叶子移动 / 歧义不匹配）、持久化往返与损坏识别（截断、坏
魔数、CRC 翻转、垃圾字节）、原子写失败不破坏旧文件、并发聚合与
持续一致性快照查询。
