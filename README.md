# query-optimizer-go

进程内查询优化器与执行计划器（Go，无外部依赖）。SQL 子集 → 逻辑计划
→ 规则改写（谓词下推 / 投影裁剪 / 连接重排）→ 基于统计信息的代价选
择（连接顺序、连接算法、顺序/索引访问路径）→ epoch 校验的计划缓存
→ 可取消的执行器。优化结果与朴素全量执行逐条一致。

详细语义、代价模型与配置说明见 [docs/OPTIMIZER.md](docs/OPTIMIZER.md)。

## 快速开始

```go
cat := catalog.New()
_ = cat.CreateTable("users", []catalog.Column{
    {Name: "id", Type: value.Int},
    {Name: "age", Type: value.Int},
})
t := cat.Table("users")
_ = t.Insert([]value.Value{value.FromInt(1), value.FromInt(30)})
t.SetStats(&catalog.Stats{RowCount: 1, NDV: map[string]int64{"id": 1}})

eng, _ := optimizer.NewEngine(cat, optimizer.DefaultConfig())
res, _ := eng.Execute(context.Background(),
    `SELECT id FROM users WHERE age >= 18`)
```

## 测试

```sh
go test ./...        # 单元与集成测试
go test ./... -race  # 竞态检测
```
