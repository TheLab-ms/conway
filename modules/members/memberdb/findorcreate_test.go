package memberdb_test

import (
	"context"
	"sync"
	"testing"

	"github.com/TheLab-ms/conway/modules/members"
	"github.com/TheLab-ms/conway/modules/members/memberdb"
)

func TestFindOrCreateByEmail_CreatesNewMember(t *testing.T) {
	db := members.NewTestDB(t)
	ctx := context.Background()

	id, err := memberdb.FindOrCreateByEmail(ctx, db, "alice@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	var email string
	if err := db.QueryRow("SELECT email FROM members WHERE id = ?", id).Scan(&email); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if email != "alice@example.com" {
		t.Fatalf("got email %q", email)
	}
}

func TestFindOrCreateByEmail_ReturnsExisting(t *testing.T) {
	db := members.NewTestDB(t)
	ctx := context.Background()

	id1, err := memberdb.FindOrCreateByEmail(ctx, db, "bob@example.com")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	id2, err := memberdb.FindOrCreateByEmail(ctx, db, "bob@example.com")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("expected stable id, got %d then %d", id1, id2)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM members WHERE email = ?", "bob@example.com").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 row, got %d", count)
	}
}

func TestFindOrCreateByEmail_PreservesExistingFields(t *testing.T) {
	db := members.NewTestDB(t)
	ctx := context.Background()

	res, err := db.Exec(`INSERT INTO members (email, name, confirmed) VALUES (?, ?, 1)`,
		"carol@example.com", "Carol")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	original, _ := res.LastInsertId()

	id, err := memberdb.FindOrCreateByEmail(ctx, db, "carol@example.com")
	if err != nil {
		t.Fatalf("find-or-create: %v", err)
	}
	if id != original {
		t.Fatalf("returned id %d, expected existing %d", id, original)
	}

	var name string
	var confirmed int
	if err := db.QueryRow("SELECT name, confirmed FROM members WHERE id = ?", id).Scan(&name, &confirmed); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if name != "Carol" || confirmed != 1 {
		t.Fatalf("fields clobbered: name=%q confirmed=%d", name, confirmed)
	}
}

func TestFindOrCreateByEmail_ConcurrentSafe(t *testing.T) {
	db := members.NewTestDB(t)
	ctx := context.Background()

	const n = 25
	var wg sync.WaitGroup
	ids := make([]int64, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			ids[i], errs[i] = memberdb.FindOrCreateByEmail(ctx, db, "race@example.com")
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	for i := 1; i < n; i++ {
		if ids[i] != ids[0] {
			t.Fatalf("got differing ids: %d vs %d", ids[0], ids[i])
		}
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM members WHERE email = 'race@example.com'").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row, got %d", count)
	}
}
