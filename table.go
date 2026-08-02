package dba

// TableDef builds a set of Node variables for a table, enabling Add("select ${u.*} from ${u}").
// Usage:
//
//	Vars(Table("user", "u").Fields("id", "name").Build())
type TableDef struct {
	table  string
	alias  string
	fields []string
}

// Table creates a table alias definition builder.
func Table(table, alias string) *TableDef {
	return &TableDef{table: table, alias: alias}
}

func (t *TableDef) Alias(alias string) *TableDef {
	t.alias = alias
	return t
}

// Fields registers the columns included in the alias.* expansion.
func (t *TableDef) Fields(fields ...string) *TableDef {
	t.fields = fields
	return t
}

// Struct extracts column names from the model's executor tags, replacing any
// previously set fields. Embed structs (like Timestamped) are expanded.
func (t *TableDef) Struct(model any) *TableDef {
	t.fields, _, _ = ColumnsAndValues(model, false)
	return t
}

// Build returns a map[string]Node suitable for SQL.Vars().
// 每条规则注册一个 ${key}, key 可以是表名、别名、带限定符的字段:
//
//	Table("users", "u").Fields("id", "name").Build()
//
//	  "u"        → Node{RawSQL: "@{1} AS @{2}",  Args: []any{"users", "u"}}    → `users` AS `u`
//	  "u.id"     → Node{RawSQL: "@{1}.@{2}",      Args: []any{"u", "id"}}       → `u`.`id`
//	  "u.name"   → Node{RawSQL: "@{1}.@{2}",      Args: []any{"u", "name"}}     → `u`.`name`
//	  "u.*"      → Node{RawSQL: "@{1}.*",          Args: []any{"u"}}             → `u`.*
//	  "users"    → Node{RawSQL: "@{1}",            Args: []any{"users"}}         → `users`
//	  "users.id" → Node{RawSQL: "@{1}.@{2}",      Args: []any{"users", "id"}}   → `users`.`id`
//	  "users.*"  → Node{RawSQL: "@{1}.*",          Args: []any{"users"}}         → `users`.*
//
// 用法:
//
//	Vars(Table("users", "u").Build())
//	Add("SELECT ${u.*} FROM ${u} LEFT JOIN @{profiles} p ON ${u.id} = p.user_id")
func (t *TableDef) Build() map[string]Node {
	m := make(map[string]Node)

	m[t.table] = Node{RawSQL: "@{1}", Args: []any{t.table}}
	m[t.table+".*"] = Node{RawSQL: "@{1}.*", Args: []any{t.table}}

	if t.alias != "" {
		m[t.alias] = Node{RawSQL: "@{1} AS @{2}", Args: []any{t.table, t.alias}}
		m[t.alias+".*"] = Node{RawSQL: "@{1}.*", Args: []any{t.alias}}
	}

	for _, f := range t.fields {
		if t.alias != "" {
			m[t.alias+"."+f] = Node{RawSQL: "@{1}.@{2}", Args: []any{t.alias, f}}
		}
		m[t.table+"."+f] = Node{RawSQL: "@{1}.@{2}", Args: []any{t.table, f}}
	}

	return m
}
