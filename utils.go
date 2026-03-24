package stupidql

import "fmt"

// Scalar 泛型函数，获取单个标量值（如 COUNT、MAX、单列查询等）
func Scalar[T any](d *StupidQL) (T, error) {
	var v T
	err := d.Get(&v)
	return v, err
}

// Page 泛型分页查询，要求 q 使用 Mark(F, ...) 标记 SELECT 字段
// 内部用 COUNT(1) 替换 F 查总数，原 query 加 LIMIT/OFFSET 查数据
func Page[T any](q *StupidQL, page, size int) ([]T, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 10
	}

	if _, ok := q.marks[F]; !ok {
		return nil, 0, fmt.Errorf("page requires Mark(F, ...) in query")
	}

	// 查总数：替换 F 为 COUNT(1)
	total, err := Scalar[int64](q.Mark(F, "COUNT(1)"))
	if err != nil || total == 0 {
		return nil, total, err
	}

	// 查数据
	var items []T
	offset := (page - 1) * size
	err = q.Add("LIMIT #{1} OFFSET #{2}", size, offset).List(&items)
	return items, total, err
}
