package dba_test

import (
	"codeberg.org/kran/dba"
	"errors"
	"testing"
)

type Item struct {
	ID   int    `db:"id,omitempty"`
	Name string `db:"name"`
	Val  int    `db:"val"`
}

func setupDao(t *testing.T) (*dba.Dao[Item], *dba.SQL) {
	t.Helper()
	q, db := newQ(t)
	_, err := db.Exec(`CREATE TABLE items (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name text NOT NULL,
		val INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		t.Fatal(err)
	}
	return dba.NewDao[Item](q, "items"), q
}

func TestDao_Create(t *testing.T) {
	dao, _ := setupDao(t)

	id, err := dao.Create(Item{Name: "foo", Val: 10})
	if err != nil {
		t.Fatal(err)
	}
	if id != 1 {
		t.Errorf("expected id 1, got %d", id)
	}

	item, _ := dao.GetByID(id)
	if item.Name != "foo" || item.Val != 10 {
		t.Errorf("got %+v", *item)
	}
}

func TestDao_Create_AutoIncrement(t *testing.T) {
	dao, _ := setupDao(t)

	id1, _ := dao.Create(Item{Name: "a", Val: 1})
	id2, _ := dao.Create(Item{Name: "b", Val: 2})

	if id1 != 1 || id2 != 2 {
		t.Errorf("expected ids 1,2 got %d,%d", id1, id2)
	}
}

func TestDao_Get(t *testing.T) {
	dao, _ := setupDao(t)
	dao.Create(Item{Name: "alice", Val: 1})
	dao.Create(Item{Name: "bob", Val: 2})

	item, err := dao.Get("name = #{1}", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if item == nil {
		t.Fatal("expected non-nil")
	}
	if item.Name != "bob" || item.Val != 2 {
		t.Errorf("got %+v", *item)
	}
}

func TestDao_Get_NotFound(t *testing.T) {
	dao, _ := setupDao(t)

	item, err := dao.Get("name = #{1}", "nobody")
	if err != nil {
		t.Fatal(err)
	}
	if item != nil {
		t.Errorf("expected nil, got %+v", *item)
	}
}

func TestDao_GetByID(t *testing.T) {
	dao, _ := setupDao(t)
	dao.Create(Item{Name: "alice", Val: 42})

	item, err := dao.GetByID(1)
	if err != nil {
		t.Fatal(err)
	}
	if item == nil {
		t.Fatal("expected non-nil")
	}
	if item.Name != "alice" {
		t.Errorf("got %+v", *item)
	}
}

func TestDao_GetByID_NotFound(t *testing.T) {
	dao, _ := setupDao(t)

	item, err := dao.GetByID(999)
	if err != nil {
		t.Fatal(err)
	}
	if item != nil {
		t.Errorf("expected nil, got %+v", *item)
	}
}

func TestDao_List(t *testing.T) {
	dao, _ := setupDao(t)
	dao.Create(Item{Name: "a", Val: 1})
	dao.Create(Item{Name: "b", Val: 2})
	dao.Create(Item{Name: "c", Val: 3})

	items, err := dao.List("val >= #{1}", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
}

func TestDao_All(t *testing.T) {
	dao, _ := setupDao(t)
	dao.Create(Item{Name: "a", Val: 1})
	dao.Create(Item{Name: "b", Val: 2})

	items, err := dao.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
}

func TestDao_Update(t *testing.T) {
	dao, _ := setupDao(t)
	dao.Create(Item{Name: "alice", Val: 1})

	affected, err := dao.Update(map[string]any{"val": 99}, "id = #{1}", 1)
	if err != nil {
		t.Fatal(err)
	}
	if affected != 1 {
		t.Errorf("expected 1 affected, got %d", affected)
	}

	item, _ := dao.GetByID(1)
	if item.Val != 99 {
		t.Errorf("expected 99, got %d", item.Val)
	}
}

func TestDao_Delete(t *testing.T) {
	dao, _ := setupDao(t)
	dao.Create(Item{Name: "a", Val: 1})
	dao.Create(Item{Name: "b", Val: 2})

	affected, err := dao.Delete("id = #{1}", 1)
	if err != nil {
		t.Fatal(err)
	}
	if affected != 1 {
		t.Errorf("expected 1 affected, got %d", affected)
	}

	count, _ := dao.CountAll()
	if count != 1 {
		t.Errorf("expected 1, got %d", count)
	}
}

func TestDao_Count(t *testing.T) {
	dao, _ := setupDao(t)
	dao.Create(Item{Name: "a", Val: 10})
	dao.Create(Item{Name: "b", Val: 20})
	dao.Create(Item{Name: "c", Val: 30})

	count, err := dao.Count("val > #{1}", 15)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}
}

func TestDao_Exists(t *testing.T) {
	dao, _ := setupDao(t)
	dao.Create(Item{Name: "alice", Val: 1})

	exists, err := dao.Exists("name = #{1}", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("expected exists")
	}

	exists, _ = dao.Exists("name = #{1}", "nobody")
	if exists {
		t.Error("expected not exists")
	}
}

func TestDao_WithTx_Commit(t *testing.T) {
	dao, q := setupDao(t)

	err := q.Transaction(func(tx *dba.SQL) error {
		txDao := dao.WithTx(tx)
		if _, err := txDao.Create(Item{Name: "a", Val: 1}); err != nil {
			return err
		}
		if _, err := txDao.Create(Item{Name: "b", Val: 2}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	count, _ := dao.CountAll()
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}
}

func TestDao_WithTx_Rollback(t *testing.T) {
	dao, q := setupDao(t)

	dao.Create(Item{Name: "existing", Val: 0})

	_ = q.Transaction(func(tx *dba.SQL) error {
		txDao := dao.WithTx(tx)
		txDao.Create(Item{Name: "will_rollback", Val: 99})
		return errors.New("fail")
	})

	count, _ := dao.CountAll()
	if count != 1 {
		t.Errorf("expected 1 (rolled back), got %d", count)
	}
}

func TestDao_CrossDao_Transaction(t *testing.T) {
	q, db := newQ(t)
	db.Exec(`CREATE TABLE users2 (id INTEGER PRIMARY KEY AUTOINCREMENT, name text)`)
	db.Exec(`CREATE TABLE orders2 (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, product text)`)

	type User2 struct {
		ID   int    `db:"id,omitempty"`
		Name string `db:"name"`
	}
	type Order2 struct {
		ID      int    `db:"id,omitempty"`
		UserID  int    `db:"user_id"`
		Product string `db:"product"`
	}

	userDao := dba.NewDao[User2](q, "users2")
	orderDao := dba.NewDao[Order2](q, "orders2")

	err := q.Transaction(func(tx *dba.SQL) error {
		userID, err := userDao.WithTx(tx).Create(User2{Name: "alice"})
		if err != nil {
			return err
		}

		_, err = orderDao.WithTx(tx).Create(Order2{UserID: int(userID), Product: "widget"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	uCount, _ := userDao.CountAll()
	oCount, _ := orderDao.CountAll()
	if uCount != 1 || oCount != 1 {
		t.Errorf("expected 1 user + 1 order, got %d + %d", uCount, oCount)
	}
}

func TestDao_CustomPrimaryKey(t *testing.T) {
	q, db := newQ(t)
	db.Exec(`CREATE TABLE configs (key text PRIMARY KEY, val text)`)

	type Config struct {
		Key string `db:"key"`
		Val string `db:"val"`
	}

	dao := dba.NewDao[Config](q, "configs").PrimaryKey("key")

	// CustomPK 非 int64，用 CreateRaw 代替
	dao.CreateRaw(Config{Key: "theme", Val: "dark"}).Exec()
	dao.CreateRaw(Config{Key: "lang", Val: "zh"}).Exec()

	cfg, err := dao.GetByID("theme")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Val != "dark" {
		t.Errorf("expected dark, got %q", cfg.Val)
	}
}

func TestDao_CreateRaw(t *testing.T) {
	dao, _ := setupDao(t)

	result, err := dao.CreateRaw(Item{Name: "raw", Val: 5}).Exec()
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	if id != 1 {
		t.Errorf("expected id 1, got %d", id)
	}
}

// ==================== Hook 测试 ====================

type HookItem struct {
	ID   int    `db:"id,omitempty"`
	Name string `db:"name"`
	Val  int    `db:"val"`
}

func (h *HookItem) BeforeCreate() error {
	if h.Name == "" {
		return errors.New("name is required")
	}
	h.Name = "hook_" + h.Name // 钩子修改字段
	return nil
}

func (h *HookItem) BeforeUpdate() error {
	if h.Val < 0 {
		return errors.New("val must be non-negative")
	}
	return nil
}

func setupHookDao(t *testing.T) *dba.Dao[HookItem] {
	t.Helper()
	q, db := newQ(t)
	_, err := db.Exec(`CREATE TABLE hook_items (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name text NOT NULL,
		val INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		t.Fatal(err)
	}
	return dba.NewDao[HookItem](q, "hook_items")
}

func TestDao_Hook_OnCreate_ModifiesField(t *testing.T) {
	dao := setupHookDao(t)
	id, err := dao.Create(HookItem{Name: "alice", Val: 1})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := dao.GetByID(id)
	if item.Name != "hook_alice" {
		t.Errorf("expected hook_alice, got %q", item.Name)
	}
}

func TestDao_Hook_OnCreate_Pointer(t *testing.T) {
	dao := setupHookDao(t)
	id, err := dao.Create(&HookItem{Name: "bob", Val: 2})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := dao.GetByID(id)
	if item.Name != "hook_bob" {
		t.Errorf("expected hook_bob, got %q", item.Name)
	}
}

func TestDao_Hook_OnCreate_Error(t *testing.T) {
	dao := setupHookDao(t)
	_, err := dao.Create(HookItem{Name: "", Val: 1})
	if err == nil || err.Error() != "name is required" {
		t.Errorf("expected 'name is required', got %v", err)
	}
}

func TestDao_Hook_OnCreate_Map_SkipsHook(t *testing.T) {
	dao := setupHookDao(t)
	id, err := dao.Create(map[string]any{"name": "raw", "val": 1})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := dao.GetByID(id)
	if item.Name != "raw" {
		t.Errorf("expected raw (no hook), got %q", item.Name)
	}
}

func TestDao_Hook_OnUpdate_Error(t *testing.T) {
	dao := setupHookDao(t)
	dao.Create(HookItem{Name: "x", Val: 1})

	_, err := dao.Update(HookItem{Name: "x", Val: -1}, "id = #{1}", 1)
	if err == nil || err.Error() != "val must be non-negative" {
		t.Errorf("expected 'val must be non-negative', got %v", err)
	}
}

func TestDao_Hook_OnUpdate_Map_SkipsHook(t *testing.T) {
	dao := setupHookDao(t)
	dao.Create(HookItem{Name: "x", Val: 1})

	affected, err := dao.Update(map[string]any{"val": -1}, "id = #{1}", 1)
	if err != nil {
		t.Fatal(err)
	}
	if affected != 1 {
		t.Errorf("expected 1 affected, got %d", affected)
	}
	item, _ := dao.GetByID(1)
	if item.Val != -1 {
		t.Errorf("expected -1 (no hook), got %d", item.Val)
	}
}

func TestDao_Hook_CreateRaw_ModifiesField(t *testing.T) {
	dao := setupHookDao(t)
	result, err := dao.CreateRaw(HookItem{Name: "raw", Val: 5}).Exec()
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	item, _ := dao.GetByID(id)
	if item.Name != "hook_raw" {
		t.Errorf("expected hook_raw, got %q", item.Name)
	}
}

func TestDao_Hook_CreateRaw_Error(t *testing.T) {
	dao := setupHookDao(t)
	_, err := dao.CreateRaw(HookItem{Name: "", Val: 1}).Exec()
	if err == nil || err.Error() != "name is required" {
		t.Errorf("expected 'name is required', got %v", err)
	}
}

func TestDao_Q_CustomQuery(t *testing.T) {
	dao, _ := setupDao(t)
	dao.Create(Item{Name: "a", Val: 10})
	dao.Create(Item{Name: "b", Val: 20})

	// 使用 Q() 构建自定义查询
	sum, _, err := dba.Scalar[int64](dao.Q().Add("SELECT SUM(val) FROM items"))
	if err != nil {
		t.Fatal(err)
	}
	if sum != 30 {
		t.Errorf("expected 30, got %d", sum)
	}
}
