# stupidql CHECKLIST

> SQL 查询构建器 — immutable chainable SQL builder for sqlx

## API Completeness

- [x] 链式 SQL 构建（`SQL` struct）
- [x] 命名占位符 `#{N}` — 自动转对应驱动格式
- [x] 变量替换 `${F:...}`
- [x] 分页查询 `Page[T]()`
- [x] 标量查询 `Scalar[T]()`
- [x] 泛型 DAO（`Dao[T]`）— CRUD + hook + transaction
- [x] Hook 中间件链（onion 模型）
- [x] Insert/Update/BatchInsert/BatchCreate
- [x] 占位符策略：`DollarFormat`（PG）、`QmarkFormat`（MySQL）
- [x] 引用策略：`AnsiQuoter`、`MySQLQuoter`
- [x] `ToMap` / `ExtractColsVals` 结构体转换
- [x] `IsOk` 零值检测

## Missing Features

- [ ] `JOIN` 辅助方法（目前依赖手写 SQL）
- [ ] 事务 helper `WithTx` 在 `SQL` 层面简洁封装（`Dao` 有，底层 `DBA` 无）
- [ ] `BatchInsert` 大数量分片（chunk）逻辑
- [ ] 日志钩子（`loghook.go`）抽样/分级策略

## Performance

- [ ] 反射热点识别 + 缓存（`utils.go` `reflect.ValueOf` 在热路径）
- [ ] `BatchInsert` chunk size 可配置
- [ ] 占位符替换避免重复 alloc

## Testing

- [ ] 覆盖所有 builder 路径
- [ ] 集成测试覆盖 PostgreSQL + MySQL 占位符格式
- [ ] 自定义 hook 执行顺序验证
- [ ] 事务回滚测试
- [ ] 分页查询边界条件（0 page, negative limit）

## Documentation

- [x] readme.md 与 go doc 一致
- [x] 所有 exported 类型有 doc comments
