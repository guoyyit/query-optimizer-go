# 查询优化器与执行计划器

本仓库实现了一套完全进程内的查询优化链路：

```
SQL 文本 → 解析(AST) → 逻辑计划 → 规则改写 → 代价选择物理计划 → 计划缓存 → 执行
```

不依赖任何外部服务；数据来自进程内 `catalog` 表。

## 支持的查询子集

```sql
SELECT * | col [, col ...]
FROM t [alias] [JOIN t [alias] ON cond | , t [alias]] ...
[WHERE cond]
```

`cond` 支持 `= != < <= > >=`、`AND / OR / NOT`、括号；字面量支持整数、
浮点、单引号字符串、`TRUE / FALSE / NULL`、负数。

## 优化语义

### 值语义（等价性的基础）

朴素执行与优化执行共用同一套求值器（`expr` + `value` 包），因此下列
语义对两条路径完全一致：

- **三值逻辑**：谓词结果为 TRUE / FALSE / UNKNOWN(NULL)。WHERE/ON 只
  保留 TRUE 行。`NOT UNKNOWN = UNKNOWN`。
- **类型强制**：`INT` 与 `FLOAT` 互相比较时 INT 提升为 FLOAT；其余
  跨类型比较（如 INT vs STRING）结果为 UNKNOWN，等值判断为 false，
  即该行被过滤。NULL 与任何值比较都是 UNKNOWN。
- **哈希键**：`HashKey` 对 INT/FLOAT 做归一化，保证 `1 = 1.0` 在哈希
  连接与索引探测中命中同一桶；哈希冲突在执行时用 `Equal` 二次校验。
- **结果顺序**：不带 ORDER BY 时，引擎对最终结果做规范化排序
  （NULL < 数值 < 字符串 < 布尔，同类型按值），因此优化路径与朴素
  路径的结果**逐条一致**，可复现。

### 规则

1. **谓词下推**（`Rules.PredicatePushdown`）
   - 只引用单侧列的合取项下推到对应输入，直到贴到 Scan 上；
   - `leftCol = rightCol` 形式的 WHERE 谓词改写为连接条件（使哈希连接
     可用）；
   - 无法下推的谓词（跨两侧但不是等值键，如 `t1.id < t2.id`）保留为
     连接上方的残余过滤，并在 NOTES 中逐条解释原因。
2. **投影裁剪**（`Rules.ProjectionPruning`）
   - 自顶向下收集各算子真正需要的列，Scan 只读所需列；裁剪动作写入
     NOTES。
3. **连接重排**（`Rules.JoinReordering`）
   - 把内连接链打平成叶子集合 + 条件池，按（估计行数, 别名）字典序
     贪心选择下一个加入的叶子，优先选择有等值条件相连的叶子；
   - 仅当剩余叶子与已连接集合之间**不存在任何连接条件**时才保留笛
     卡尔积，并在 NOTES 中说明这是唯一语义正确的选择；
   - 单个叶子的条件（如 `ON a.x = 5`）改写为该叶子上的过滤；
   - 无法归属到单个连接的条件（引用三张及以上表）成为顶层残余过滤。

规则可独立开关；关闭后行为可预测：下推关闭则谓词留在原位置，重排
关闭则保持 FROM 顺序，裁剪关闭则 Scan 读全列。无论开关如何组合，
执行结果与朴素全量执行逐条一致（见 `TestEquivalenceAcrossRuleToggles`）。

## 代价模型

物理规划器为每个节点选择访问路径、为每个连接选择算法与构建侧：

| 权重 | 含义 | 默认值 |
|---|---|---|
| `SeqScanRow` | 顺序扫描每行 | 1.0 |
| `IndexProbe` | 一次哈希索引探测固定成本 | 8.0 |
| `IndexRow` | 索引回表每行 | 0.5 |
| `FilterRow` | 谓词求值每行 | 0.1 |
| `HashBuildRow` | 哈希表构建每行 | 1.5 |
| `HashProbeRow` | 哈希探测每行 | 0.5 |
| `NestedLoop` | 嵌套循环每行对 | 1.0 |
| `OutputRow` | 每输出行 | 0.05 |

估计公式：

- 索引扫描：`IndexProbe + (rows/NDV) * IndexRow`
- 哈希连接：`build*HashBuildRow + probe*HashProbeRow + out*OutputRow`
- 嵌套循环：`left*right*NestedLoop + out*OutputRow`
- 连接输出行数：`|L|*|R| / max(NDV_l, NDV_r)`（每个等值键除一次）
- 选择率：等值 `1/NDV`，范围 `1/3`，NOT 取补，AND 相乘，OR 相加（封顶 1）

### 确定性裁决规则（文档化的 tie-break）

所有候选计划按 **(代价, 计划指纹)** 二元组比较：代价相同（含浮点完全
相等）时，**指纹字符串字典序较小者胜**。指纹是计划形状的规范化串
（如 `hashjoin(seq(a:...),seq(b:...))`），AND/OR 子项在指纹前已按字典
序排序。因此：

> 相同查询 + 相同统计信息 + 相同配置 ⇒ 无论多少并发、什么执行顺序，
> 永远选出同一条物理计划。

连接重排的贪心选择同理按（估计行数, 别名）字典序裁决。

## 统计信息缺失/过期的降级

- 统计缺失（`SetStats(nil)` 或从未设置）或超过 `StatsMaxAge` 未更新时，
  该表按 `DefaultRows` / `DefaultNDV` 的保守估计规划；
- 每一次降级都记录到 `PlannedQuery.StatsMissing`，并在 EXPLAIN 输出的
  `MARKERS` 段可见，**不会静默套用错误代价**；
- 降级只影响计划选择，不影响结果正确性。

## 计划缓存与失效

- 缓存键 = 查询指纹（AND/OR 已规范化）+ 配置戳（规则开关与代价权重）；
  改配置不会复用旧计划；
- 每个缓存条目记录生成时的 catalog epoch；任何建表/插入/统计变更/建
  索引都会推进 epoch，epoch 不匹配的条目视为不存在并惰性清除——
  **统计或表结构变化后旧计划必然失效**；
- 并发同一查询的规划通过 singleflight 合并：只有第一个调用真正规划，
  其余等待共享结果；条目在锁外构建、单次 map 写入发布，读者永远读
  不到半更新状态；
- 规划失败或被取消的结果**不写入**缓存；
- 容量按 FIFO 淘汰，`CacheSize = 0` 关闭缓存。

## 并发与取消

- `Engine` 全部方法可并发调用；
- 规划并发度受 `MaxPlans` 信号量限制，慢查询的规划不会阻塞其他查询
  （超出并发度才排队，且排队本身可被 ctx 取消）；
- 每次规划受 `PlanTimeout` 约束（0 表示不限制）；
- 执行器每 256 行检查一次 `ctx.Err()`；取消/超时后返回错误且**不返回
  任何部分结果**，也不污染计划缓存。

## 配置项（`optimizer.Config`）

| 字段 | 含义 | 默认 |
|---|---|---|
| `Rules` | 三条规则的独立开关 | 全开 |
| `Weights` | 上表八个代价权重 | 见上表 |
| `StatsMaxAge` | 统计保鲜期，过期视为缺失 | 5min |
| `DefaultRows` | 统计缺失时的行数估计 | 1000 |
| `DefaultNDV` | 统计缺失时的 NDV 估计 | 10 |
| `CacheSize` | 计划缓存容量，0 关闭 | 128 |
| `MaxPlans` | 最大并发规划数，<=0 取 4 | 4 |
| `PlanTimeout` | 单次规划超时，0 关闭 | 10s |

`Config.Validate()` 对每个非法字段返回 `*ConfigError{Field, Reason}`，
可区分是哪个配置项出错（权重非法会进一步指明具体权重名）。

## 计划输出（EXPLAIN）

`PlannedQuery.Explain` 示例：

```
PLAN
  Project [u.name, o.total]
    HashJoin keys L=[o.uid] R=[u.id]
      Filter (o.total < 200)
        SeqScan orders AS o cols=[o.uid, o.total]
      Filter (u.age >= 18)
        SeqScan users AS u cols=[u.id, u.age, u.name]
NOTES
  [predicate-pushdown] pushed 1 predicate(s) to left input of join
  [join-reorder] join order: [o u]
  [join-algorithm] hash join: left input builds (cost 3422.2 vs 3422.2)
MARKERS
  stats-missing: o (conservative defaults used)
```

## 测试覆盖

- `equivalence_test.go`：NULL、INT/FLOAT 混合、int64 边界值、跨表重名
  列、类型不一致比较、空结果、规则开关全组合 —— 均与朴素执行逐条一致；
- `rules_test.go`：谓词下推位置、连接重排顺序、笛卡尔积解释、残余谓
  词解释、投影裁剪；
- `determinism_test.go`：并发下计划指纹一致、代价相同裁决稳定、统计
  缺失/过期标记、统计与数据变化后的缓存失效、配置变更不复用旧计划；
- `concurrency_test.go`：取消无部分结果、慢查询不阻塞规划、并发读写
  统计下结果始终正确；
- `index_test.go` / `config_test.go`：索引选择、残留下推、权重改变计
  划、配置错误分类。
