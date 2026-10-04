# sampling-profiler-go

面向采样剖析数据的聚合与对比库。上游多个执行单元持续上报带时间戳、单元标识
与调用栈的采样记录；本库把它们聚合为每个调用路径的自身耗时（self）与累计耗时
（cumulative），支持乱序/重复/迟到采样、栈合并与截断、降采样、跨版本对比、
持久化恢复以及并发聚合。整条链路只在本进程内运行，不依赖任何外部服务。

## 核心语义

- **调用栈以叶子优先（leaf-first）存储**：`Stack[0]` 是采样观测到的最内层帧。
- **自身耗时 Self**：叶子节点（深度 0）的 Self 等于以该帧为叶子的采样权重之和。
- **累计耗时 Cum**：节点的 Cum 等于经过该路径（叶子到该帧）的采样权重之和。
  同一采样在其路径的每个节点上各计一次 Cum，在叶子节点计一次 Self。
- **确定性**：聚合是对唯一采样的、顺序无关的求和归约。无论采样以什么顺序到达、
  如何分批提交（Commit）、以多少并发摄入，只要采样集合相同，结果逐节点一致、
  可复现。
- **去重**：采样键为 `ID`（若提供），否则为 `Unit@TS`。重复上报只计一次；
  同一执行单元同一时刻的采样不会被重复累计。
- **迟到采样**：任何时候到达的新采样都会在下一次 Commit 时聚合，不会被丢弃；
  已提交的快照不可变，查询要么看到提交前状态、要么看到提交后状态，不会读到
  半成品。

## 栈处理流水线（Config）

归一化在摄入时对每个采样执行，顺序固定为：

1. **未知帧处理**（`Unknown`）：`KnownFrames` 非空时，不在集合中的帧按策略处理：
   - `UnknownKeep`（默认）：原样保留；
   - `UnknownPlaceholder`：替换为 `Placeholder`（默认 `<unknown>`），多个未知帧
     因此合并为同一节点。
2. **重复帧折叠**（`Recursion`）：`RecursionFold` 把连续重复帧（如
   `leaf,leaf,leaf,root`）折叠为 `leaf,root`；`RecursionKeep`（默认）原样保留。
3. **深度截断**（`MaxDepth`，0 为不限制）：只保留叶子侧最深的 `MaxDepth` 帧。
   截断只丢弃根侧帧，因此所有保留节点的 Self/Cum 与截断前完全一致。

## 降采样与裁剪

- **降采样**（`DownsampleK`，默认 1 即关闭）：对采样键做确定性哈希，每 K 个
  采样保留 1 个。决策只依赖采样键本身，与到达顺序、分批、并发无关，因此结果
  可复现。被丢弃的采样计入 `Stats.Dropped`。
- **有损去重**（`DedupLossy`）：默认 `DedupExact` 精确记录每个见过的键（内存随
  唯一采样数增长）。开启降采样后可选用 `DedupLossy`，每分片只保留容量为
  `DedupCapacity` 的 LRU 键集合，内存有界；被淘汰的键若再次到达可能被重复计入，
  淘汰数计入 `Stats.SeenEvicted`。
- **叶子裁剪**（`PruneMinCount`，默认 0 关闭）：Commit 时移除 Self 低于阈值的
  叶子及其独占路径链（叶子优先树中，以帧 L 为路径首帧的所有节点都只属于该
  叶子）。其余节点计数不变；被移除的节点数与 Self 计入
  `Stats.PrunedNodes` / `Stats.PrunedSamples`。

### 可核对的计数

`Stats` 满足恒等式：

```
Received = Duplicates + Dropped + Kept
```

每个到达的采样恰好落入其中一个桶。快照的 `Total()` 是已聚合采样的权重之和
（默认权重为 1）。

## 版本对比（Compare）

`Compare(before, after, CompareConfig)` 逐节点给出 Self/Cum 的增减（`DeltaSelf`、
`DeltaCum`），节点匹配规则确定且可解释：

1. **精确匹配**（`MatchExact`，默认）：按归一化后的完整路径匹配。
2. **叶子名匹配**（`MatchLeaf`）：精确匹配后仍未配对的节点，若叶子帧名相同且
   两侧各只有一个候选，则配为一对（`MatchKindLeaf`），用于叶子重命名或位置
   移动；候选不唯一时不配对，避免歧义。
3. 仍未配对的节点：仅存在于 before 的为 `DiffRemoved`，仅存在于 after 的为
   `DiffAdded`。

`CompareConfig.Renames` 提供旧帧名到新帧名的映射，在匹配前应用，用于处理
**重命名**。所有 Diff 按路径字典序输出，保证结果确定。

## 持久化

- `Save`：数据先写入目标目录下的临时文件，`fsync` 后原子 rename 覆盖目标文件；
  原文件保留为 `<path>.bak`。写入中断只会留下临时文件，目标文件要么是旧版本、
  要么是完整新版本。
- 文件格式：8 字节魔数 `SPROF1` + gob 编码载荷 + 32 字节 SHA-256 校验和。
- `Load`：校验魔数与校验和；不匹配返回 `ErrCorrupt`（魔数不符返回
  `ErrBadMagic`）。若主文件损坏而 `<path>.bak` 存在，则加载备份并同时返回
  警告 `ErrLoadedFromBackup`。
- 加载后重建的快照与内存中逐节点一致（测试覆盖）。

## 并发模型

- 摄入按采样键哈希到 `ShardCount` 个分片，分片各自加锁；`Ingest` 可并发调用。
- `Commit` 串行化：交换各分片的待提交缓冲（短暂持锁），在锁外构建新快照，再
  通过原子指针发布。
- 查询（`Snapshot()`、`NodeByPath()`、`Nodes()`）读取不可变快照，无数据竞争
  （`go test -race` 覆盖），且永远看到某个完整自洽的状态。

## 可调参数一览

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `MaxDepth` | 0 | 保留叶子侧的最大帧数，0 为不限制 |
| `Recursion` | `RecursionKeep` | 重复帧折叠策略 |
| `Unknown` | `UnknownKeep` | 未知帧处理策略 |
| `KnownFrames` | nil（全部已知） | 已知帧名集合 |
| `Placeholder` | `<unknown>` | 未知帧替换名 |
| `DownsampleK` | 1 | 确定性降采样间隔，1 为关闭 |
| `Dedup` | `DedupExact` | 去重策略 |
| `DedupCapacity` | 65536 | 有损去重的每分片 LRU 容量 |
| `PruneMinCount` | 0 | 叶子 Self 裁剪阈值，0 为关闭 |
| `ShardCount` | 16 | 摄入分片数 |

## 测试

```sh
go test -race ./...
```

覆盖：确定性（顺序/分批/分片）、乱序重复与迟到、深栈截断、未知帧与递归折叠、
降采样与有损去重、叶子裁剪、版本对比（增删/重命名/移动）、持久化往返与损坏恢复、
并发聚合与查询。
