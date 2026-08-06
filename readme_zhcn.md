# dba

一个尊重 SQL 的 Go 查询构建库。

dba 不把 SQL 翻译成方法调用,也不把它藏进对象模型——SQL 由你来写,dba 只负责其余的体力活:动态条件拼接、参数编号与方言占位符、片段复用、struct/map 到 INSERT/UPDATE 的映射,以及分页这类高频协作模式。它位于 sqlx(扫描增强)与 squirrel(AST 式 builder)之间的空档,思想上接近 MyBatis 的动态 SQL,但用不可变链式 API 取代 XML。

```go
db, _ := dba.Open("pgx", dsn)

var users []User
err := db.Select("users", "status = #{1}", "active").
    AddIf(name != "", "AND name LIKE #{1}", "%"+name+"%").
    AddIf(len(ids) > 0, "AND id IN (#{1|expand})", ids).
    Add("ORDER BY created_at DESC").
    List(&users)
```

核心性质一句话:**builder 不可变**。每个方法返回新实例,半成品查询可以安全地跨函数传递、缓存、分叉——这是分页、count 复用等模式的地基。

## 安装

```
go get github.com/yourname/dba
```

依赖 `jmoiron/sqlx`。通过 `Open(driver, dsn)` 或 `NewFromSqlx(*sqlx.DB)` 创建,自动按驱动选择占位符格式(Postgres 系为 `$n`,其余为 `?`)与标识符 quoting(MySQL 反引号,其余 ANSI 双引号),可用 `Formatter`/`Quoter` 覆盖。

## 模板语言

dba 的模板只有两个核心符号,分别对应两层:

| 语法 | 层 | 含义 |
|---|---|---|
| `#{key}` | 值层 | 解析参数并绑定为占位符 |
| `#{key\|pipe}` | 值层 | 解析参数,交给指定管道处理 |
| `${var}` | 结构层 | 展开命名变量(模板递归) |
| `${var:default}` | 结构层 | 变量未定义时展开默认文本 |

### 值层:`#{}` 与管道

参数 key 有两种形态。**位置**:`#{1}`、`#{2}` 指向本片段 `Add` 的第 n 个参数——编号是 per-fragment 的,每个 `Add` 从 1 数起,片段之间互不干扰,这让片段可以自由组合而不用重排序号。**命名**:`#{name}` 从本片段最后一个参数(struct 或 map)中按名取值;struct 按 `db` tag 映射,与行扫描共用同一套规则。

```go
db.Add("WHERE age > #{1} AND city = #{2}", 18, "SH")       // 位置
db.Add("WHERE name = #{Name} AND age > #{Age}", user)       // 命名: struct
db.Add("WHERE name = #{name}", dba.H{"name": "bob"})        // 命名: map (H 是 map[string]any 别名)
db.Add("WHERE a = #{1} AND b = #{key}", 1, dba.H{"key": 2}) // 混用: 命名源取最后一个参数
```

管道决定参数值如何进入 SQL,内置五个:

```go
// bind (默认): 绑定为占位符
db.Add("WHERE id = #{1}", 42)                        // → WHERE id = $1

// expand: 切片展开为逗号分隔的独立占位符
db.Add("WHERE id IN (#{1|expand})", []int{1, 2, 3})  // → IN ($1, $2, $3)

// raw: 参数值作为 SQL 文本原样注入 (调用者自证安全, 见「安全边界」)
db.Add("WHERE created > #{1|raw}", "NOW()")           // → WHERE created > NOW()

// quote: 参数值作为标识符, 按方言 quote (动态列名/排序场景)
db.Add("ORDER BY #{1|quote}", sortCol)                // → ORDER BY "sort_col"

// literalquote: 宏内容本身即标识符 (不消耗参数)
db.Add("SELECT * FROM #{users|literalquote}")         // → SELECT * FROM "users"
```

两个简写宏:`!{1}` 等价 `#{1|raw}`,`@{users}` 等价 `#{users|literalquote}`。空切片 expand 会产出 `IN ()`,由数据库报语法错误——dba 不做拦截,动态条件请配合 `AddIf(len(ids) > 0, ...)`。

### 结构层:`${}` 变量

变量是命名的 SQL 片段(模板 + 自己的参数),渲染时递归展开,参数作用域彼此隔离:

```go
q := db.Add("SELECT * FROM t WHERE ${cond} ${order:ORDER BY id}").
    Var("cond", "status = #{1} AND ${scope}", "active").
    Var("scope", "org_id = #{1}", orgID)
// ${order:...} 未定义时落到默认值 ORDER BY id
```

变量是**晚绑定**的:先 `Add` 引用、后 `Var` 定义完全合法,查找发生在构建时。这是槽位协议(见下文 F/I/O)的基础。渲染递归有深度上限(64),自引用环会得到明确错误而非栈溢出。

## Node:参数即子树

`Expr(sql, args...)` 返回一个 `Node`——SQL 片段与它自己的参数。**Node 出现在任何参数位置时,都会被内联渲染而不是绑定为占位符**,这是整个库统一的不变式:

```go
// Add 的参数
db.Add("WHERE updated < #{1}", dba.Expr("NOW() - INTERVAL #{1} DAY", 7))
// → WHERE updated < NOW() - INTERVAL $1 DAY

// Insert/Update 的字段值
db.Update("counters", dba.H{
    "views": dba.Expr("views + #{1}", 1),  // 内联: views = views + $1
    "name":  "n",                           // 普通值: 占位符
}, "id = #{1}", 5)

// expand 的切片元素 (子查询混进 IN 列表)
keys := []any{"alice", dba.Expr("lower(#{1})", input), "bob"}
db.Add("WHERE username IN (#{1|expand})", keys)
// → IN ($1, lower($2), $3)

// struct 字段 (Node 类型字段整体作为单列)
type Event struct {
    Name    string   `db:"name"`
    Created dba.Node `db:"created"`
}
db.Insert("events", Event{Name: "e", Created: dba.Expr("NOW()")})
```

`*Node` 同样支持,nil 指针绑定为 SQL NULL。

## CRUD 生成器

```go
db.Select("users", "age > #{1}", 18)          // SELECT ${F:*} FROM "users" WHERE age > $1
db.Insert("users", user)                       // struct 或 map
db.Update("users", changes, "id = #{1}", id)   // changes 为 struct 或 map
db.Delete("users", "id = #{1}", id)
db.BatchInsert("users", entities)              // 批量, 全列 (见 omitempty 说明)
db.Add("INSERT INTO t (a, b) VALUES").Batch(rows) // 手动批量值组
```

生成器只是普通 `Add` 的封装,输出的 builder 可以继续链:

```go
db.Insert("users", u).Add("ON CONFLICT (email) DO NOTHING").Exec()
```

### struct 映射与 omitempty

列名规则与扫描、命名参数三处共用一个 mapper:`db` tag 优先(原样使用),无 tag 用字段名小写,`db:"-"` 排除。嵌套/匿名 struct 递归展开为平铺列;`driver.Valuer` 实现者、`time.Time` 及其别名、`Node` 作为原子单列不展开。

写入时的零值语义是**逐字段、声明处可见**的(有意区别于 GORM 的隐式全局零值跳过):

```
值字段, 无 tag          → 永远写入 (零值也写)
值字段 + omitempty      → 零值跳过 (自增 id / 交给 DB 默认值的列)
指针字段 + omitempty    → nil 跳过; 非 nil 保留 (即使指向零值) —— 写零的逃生舱
Node 字段               → 单列内联; 零值 Node + omitempty 跳过
map                     → 完全手动, 给什么写什么
```

指针逃生舱示例:`Views *int` 标 omitempty 后,nil 表示"未设置,跳过此列",`&zero` 表示"我就是要写 0"。`sql.NullString{Valid: false}` 是零值,标了 omitempty 会被跳过——想显式写 NULL 请用指针或 map。

`BatchInsert` 强制全列(忽略 omitempty):批量要求所有行同列集,按行跳列会导致列集漂移。

## 槽位协议:F / I / O

生成器与工具函数之间通过三个约定变量名协作,全部基于 `${var:default}` 的晚绑定:

**F —— 列集槽**。`Select` 生成 `${F:*}`,`Page` 通过 `Var(F, "COUNT(1)")` 把同一个 builder 分叉成计数查询。手写 SQL 想接入 `Page`,在主链嵌入 `${F:*}` 即可:

```go
q := db.Add(`SELECT ${F:*} FROM orders o JOIN users u ON o.uid = u.id WHERE o.status = #{1}`, st)
items, total, err := dba.Page[Order](q, 1, 20)
```

`Page` 的计数是简单替换,**不适用于 GROUP BY / DISTINCT 查询**——那类查询请自行写 count。

**O —— 排序槽**(可选优化)。把 ORDER BY 写成 `${order:ORDER BY id DESC}`,`Page` 的计数查询会清空它,省掉无意义的排序开销。裸写 ORDER BY 依然正确,只是计数多付排序。

**I —— INSERT 修饰槽**。`Insert` 生成 `INSERT ${I:} INTO ...`,默认为空。链式 `Add` 只能追加尾部(RETURNING / ON CONFLICT 天然可达),唯独 INSERT 与 INTO 之间是死角,此槽是唯一通气孔:

```go
db.Var(dba.I, "IGNORE").Insert("users", u)   // INSERT IGNORE INTO ...
```

## 执行与扫描

```go
err  := q.List(&users)              // 多行 → slice 指针 (struct/基本类型)
ms, err := q.ListMap()              // 多行 → []map[string]any (列集编译期未知时)
found, err := q.Get(&user)          // 单行; 无结果 found=false, 不返回 ErrNoRows
m, found, err := q.GetMap()         // 单行 → map
v, found, err := dba.Scalar[int64](q) // 单值
result, err := q.Exec()             // 非查询
rows, err := q.Rows()               // 原始游标 (流式)
sql, args, err := q.ToSQL()         // 只构建不执行
```

builder 上的错误(生成器入参非法、Batch 宽度不齐等)沿链累积,后续操作空转,在执行或 `ToSQL` 时统一返回;也可随时 `q.Error()` 检查。

### 事务

```go
err := db.Transaction(func(tx *dba.SQL) error {
    if _, err := tx.Insert("orders", order).Exec(); err != nil {
        return err
    }
    _, err := tx.Update("stock", dba.H{"n": dba.Expr("n - #{1}", 1)}, "sku = #{1}", sku).Exec()
    return err // 返回 error 或 panic 均回滚
})
```

已在事务中时 `Transaction` 直接执行函数体,不做嵌套。也可手动 `Begin`/`Commit`/`Rollback`。

### 日志

```go
db = db.SetLogger(func(ctx context.Context, begin time.Time, query string, args []any, err error) {
    slog.Info("sql", "cost", time.Since(begin), "query", query, "err", err)
})
```

回调在每次执行后触发,不改变执行流。

## Dao:泛型单表助手

`Dao[T]` 把"一张表的常规操作"收拢到一处,表名与主键单点维护:

```go
type User struct {
    ID      int64  `db:"id,omitempty"`
    Email   string `db:"email"`
    Name    string `db:"name"`
    Deleted int    `db:"deleted"`
}

// 可选钩子: 实现即生效
func (u *User) BeforeCreate() error {
    if u.Email == "" { return errors.New("email required") }
    return nil
}

userDao := dba.NewDao[User](db, "users")          // 默认主键 id, 可 .PK("uid") 覆盖

id, err := userDao.Create(&u)                      // 返回自增/RETURNING 主键
u, err  := userDao.GetByID(42)                     // 未找到返回 (nil, nil), 先判 nil
u, err  := userDao.Get("email = #{1}", email)
list, err := userDao.List("deleted = 0")
n, err  := userDao.Update(dba.H{"name": "x"}, "id = #{1}", 42)
n, err  := userDao.Delete("id = #{1}", 42)
ok, err := userDao.Exists("email = #{1}", email)
items, total, err := userDao.Page(1, 20, "deleted = 0")
n, err  := userDao.Batch(users)                    // 批量插入, 返回影响行数
```

设计要点:

**未找到不是错误。** `Get`/`GetByID` 无结果返回 `(nil, nil)` 而非 `sql.ErrNoRows`——"没查到"是正常业务结果,调用方判 nil 即可,不必到处 `errors.Is`。

**Raw 系列保留完整链式能力。** `RawCreate`/`RawSelect`/`RawBatch` 返回 builder 而非直接执行,复杂需求(ON CONFLICT、RETURNING、追加条件)在 Dao 之上继续拼:

```go
err := userDao.RawCreate(&u).
    Add("ON CONFLICT (email) DO UPDATE SET name = EXCLUDED.name").
    Add("RETURNING " + "id").
    Get(&u.ID)
```

**事务传递。** `dao.WithTx(tx)` 返回绑定到事务的 Dao 副本,原 Dao 不受影响:

```go
db.Transaction(func(tx *dba.SQL) error {
    d := userDao.WithTx(tx)
    // d 上的所有操作走事务
    return nil
})
```

**跨表 JOIN 的别名变量。** `dao.Vars(alias)` 生成表声明/别名/主键三个引用变量,表名与主键改动只需动 Dao 一处:

```go
q := db.Add(`SELECT ${u}.*, ${o}.total
             FROM ${u.as} JOIN ${o.as} ON ${o}.uid = ${u.pk}
             WHERE ${u}.status = #{1}`, "active").
    Vars(userDao.Vars("u")).
    Vars(orderDao.Vars("o"))
// ${u.as} → "users" AS "u"    ${u} → "u"    ${u.pk} → "u"."id"
```

列引用不在 Vars 生成范围内(`${u}.email` 裸写)——列集不属于 Dao 的维护职责。

## 安全边界

请把这一节当作使用前提而不是附注。

**参数走 `#{}` 就是安全的**——所有 bind/expand 路径最终都是驱动层占位符,无拼接。风险集中在三个显式的"文本注入"入口,它们存在的意义是灵活性,安全责任在调用者:

其一,**模板本身不可拼接不可信输入**。`Add("WHERE name = '" + userInput + "'")` 在任何库里都是注入;在 dba 里问题更早——userInput 中的 `#{`、`${` 会被当作宏解析。规则:模板字符串必须是代码里的字面量或可信来源,用户数据永远走参数位。

其二,**raw 管道(`!{}`)是唯一的任意文本注入口**。只用于代码可控的 SQL 表达式(`NOW()`、`DEFAULT`),绝不传用户输入。审计时全局搜索 `|raw` 与 `!{` 即可枚举全部注入面。

其三,**quote/literalquote 做方言转义但不做白名单**。`#{1|quote}` 能防止标识符逃逸,但用户仍可指定任意列名(信息泄露面)。动态排序列请先过白名单再进管道。

## 扩展

自定义管道通过 `RegisterPipe` 注册(实例级,copy-on-write,无全局状态):

```go
db = db.RegisterPipe("upper", func(ctx dba.RenderCtx, content string) error {
    v, err := ctx.Resolve(content)   // 解析参数
    if err != nil { return err }
    return ctx.Bind(strings.ToUpper(fmt.Sprint(v))) // Bind: Node 内联, 普通值占位符
})
db.Add("WHERE name = #{1|upper}", "bob")   // 绑定 "BOB"
```

管道内对参数值一律走 `ctx.Bind`,自动继承 Node 内联语义。

## 非目标

dba 有意不做以下事情,提交这些方向的功能请求前请先读此节:

不做关系与预加载——JOIN 由你写,dba 提供 `Vars(alias)` 帮你维护引用,仅此而已。不做数据库迁移。不做跨方言 SQL 翻译——你写的方言 SQL 原样送出,dba 只处理占位符与标识符 quoting 两处方言差异。不做查询缓存。不拦截空 `IN ()`——那是数据库的语法错误,dba 不替你猜意图。不做嵌套事务/保存点。不做模型校验——`BeforeCreate`/`BeforeUpdate` 钩子是留给你自己做的口子。

## 附录:宏前缀注册(兼容性保留)

`RegisterMacro(prefix, pipe)` 允许注册自定义宏前缀(如 `^{1}` 等价 `#{1|upper}`)。**此机制为既有项目保留;新代码请直接使用 `#{key|pipe}`,功能完全等价且无需读者查表。** `#` 与 `$` 为保留前缀,内置的 `@`/`!` 亦通过此机制实现。

```go
db = db.RegisterMacro('^', "upper")   // ^{1} ≡ #{1|upper}
```