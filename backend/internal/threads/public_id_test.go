package threads

import (
	"context"
	"regexp"
	"testing"
)

// address is the shape the whole feature rests on: 22 URL-safe characters, the
// same 128 bits a share token is minted from.
var address = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)

func TestCreate_mintsAnAddressShapedLikeAShareToken(t *testing.T) {
	s, ctx, _, _ := newThreadStore(t)

	first, err := s.Create(ctx, testSubject, "frage")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second, err := s.Create(ctx, testSubject, "frage")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if !address.MatchString(first.PublicID) {
		t.Errorf("public id = %q, want 22 URL-safe characters", first.PublicID)
	}
	if first.PublicID == second.PublicID {
		t.Error("two threads were minted the same address")
	}
}

func TestList_carriesTheAddress(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)

	list, err := s.List(ctx, testSubject)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("threads = %+v, want one", list)
	}
	// The rail renders these rows and every click builds a URL out of the id;
	// a row without one is a thread nothing can open.
	if !address.MatchString(list[0].PublicID) {
		t.Errorf("listed thread has no address: %+v", list[0])
	}
	if list[0].ID != th {
		t.Errorf("row id = %d, want %d", list[0].ID, th)
	}
}

func TestResolve_findsTheThreadAndSaysNothingAboutTheOnesItDoesNot(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	list, _ := s.List(ctx, testSubject)

	id, ok, err := s.Resolve(ctx, list[0].PublicID)
	if err != nil || !ok || id != th {
		t.Fatalf("resolve = %d, %v, %v; want %d, true, nil", id, ok, err, th)
	}

	// Unknown and empty are both "no thread", never an error: the handler turns
	// them into the same 404 a thread belonging to someone else gets.
	for name, addr := range map[string]string{
		"unknown": "AAAAAAAAAAAAAAAAAAAAAA",
		"empty":   "",
	} {
		if _, ok, err := s.Resolve(ctx, addr); ok || err != nil {
			t.Errorf("%s address = %v, %v; want false, nil", name, ok, err)
		}
	}
}

func TestPublicIDFor_isTheWayBackFromARowID(t *testing.T) {
	s, ctx, th, _ := newThreadStore(t)
	list, _ := s.List(ctx, testSubject)

	got, err := s.PublicIDFor(ctx, th)
	if err != nil {
		t.Fatalf("public id for: %v", err)
	}
	if got != list[0].PublicID {
		t.Errorf("address = %q, want %q", got, list[0].PublicID)
	}
	// A row that is gone has no address, and that is not an error: the caller
	// is a stream whose thread was deleted mid-turn.
	if got, err := s.PublicIDFor(ctx, th+9999); err != nil || got != "" {
		t.Errorf("address of a thread that is gone = %q, %v; want \"\", nil", got, err)
	}
}

// A database that cannot be read is an ERROR on every one of these, never a
// quiet "no such thread": the handler above turns the first into a 500 and the
// second into a 404, and a locked database answering 404 would tell a reader
// their thread is gone.
func TestPublicID_aShutDatabaseIsAnErrorNotASilentMiss(t *testing.T) {
	s, ctx, th, db := newThreadStore(t)
	list, _ := s.List(ctx, testSubject)
	addr := list[0].PublicID
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if _, _, err := s.Resolve(context.Background(), addr); err == nil {
		t.Error("resolve on a shut database reported no thread instead of failing")
	}
	if _, err := s.PublicIDFor(context.Background(), th); err == nil {
		t.Error("public id for on a shut database reported no address instead of failing")
	}
	if _, err := s.Create(context.Background(), testSubject, "frage"); err == nil {
		t.Error("create on a shut database reported success")
	}
}
