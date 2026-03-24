package stupidql_test

import (
	"errors"
	"testing"

	"codeberg.org/kran/stupidql"
)

type Item struct {
	ID   int    `db:"id,omitempty"`
	Name string `db:"name"`
	Val  int    `db:"val"`
}

func setupDao(t *testing.T) (*stupidql.Dao[Item], *stupidql.StupidQL) {
	t.Helper()
	q, db := newQ(t)
	_, err := db.Exec(`CREATE TABLE items (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		val INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		t.Fatal(err)
	}
	return stupidql.NewDao[Item](q, "items"), q
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
		t.Errorf("got %+v", item)
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
	if item.Name != "bob" || item.Val != 2 {
		t.Errorf("got %+v", item)
	}
}

func TestDao_GetByID(t *testing.T) {
	dao, _ := setupDao(t)
	dao.Create(Item{Name: "alice", Val: 42})

	item, err := dao.GetByID(1)
	if err != nil {
		t.Fatal(err)
	}
	if item.Name != "alice" {
		t.Errorf("got %+v", item)
	}
}

func TestDao_FindByID_Found(t *testing.T) {
	dao, _ := setupDao(t)
	dao.Create(Item{Name: "alice", Val: 42})

	item, err := dao.FindByID(1)
	if err != nil {
		t.Fatal(err)
	}
	if item == nil {
		t.Fatal("expected non-nil")
	}
	if item.Name != "alice" || item.Val != 42 {
		t.Errorf("got %+v", *item)
	}
}

func TestDao_FindByID_NotFound(t *testing.T) {
	dao, _ := setupDao(t)

	item, err := dao.FindByID(999)
	if err != nil {
		t.Fatal(err)
	}
	if item != nil {
		t.Errorf("expected nil, got %+v", *item)
	}
}

func TestDao_Find_Found(t *testing.T) {
	dao, _ := setupDao(t)
	dao.Create(Item{Name: "alice", Val: 1})

	item, err := dao.Find("name = #{1}", "alice")
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

func TestDao_Find_NotFound(t *testing.T) {
	dao, _ := setupDao(t)

	item, err := dao.Find("name = #{1}", "nobody")
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

	err := q.Transaction(func(tx *stupidql.StupidQL) error {
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

	_ = q.Transaction(func(tx *stupidql.StupidQL) error {
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
	db.Exec(`CREATE TABLE users2 (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT)`)
	db.Exec(`CREATE TABLE orders2 (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, product TEXT)`)

	type User2 struct {
		ID   int    `db:"id,omitempty"`
		Name string `db:"name"`
	}
	type Order2 struct {
		ID      int    `db:"id,omitempty"`
		UserID  int    `db:"user_id"`
		Product string `db:"product"`
	}

	userDao := stupidql.NewDao[User2](q, "users2")
	orderDao := stupidql.NewDao[Order2](q, "orders2")

	err := q.Transaction(func(tx *stupidql.StupidQL) error {
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
	db.Exec(`CREATE TABLE configs (key TEXT PRIMARY KEY, val TEXT)`)

	type Config struct {
		Key string `db:"key"`
		Val string `db:"val"`
	}

	dao := stupidql.NewDao[Config](q, "configs").PrimaryKey("key")

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

func TestDao_Q_CustomQuery(t *testing.T) {
	dao, _ := setupDao(t)
	dao.Create(Item{Name: "a", Val: 10})
	dao.Create(Item{Name: "b", Val: 20})

	// 使用 Q() 构建自定义查询
	sum, err := stupidql.Scalar[int64](dao.Q().Add("SELECT SUM(val) FROM items"))
	if err != nil {
		t.Fatal(err)
	}
	if sum != 30 {
		t.Errorf("expected 30, got %d", sum)
	}
}
