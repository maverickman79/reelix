package repository_test

import (
	"context"
	"errors"
	"testing"
	"uuid"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/repository"
)

func TestUserRepositoryCreateAndGet(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	users := repository.NewUserRepository(pool)

	u := domain.User{
		Username:     "admin",
		PasswordHash: "$argon2id$v=19$m=65536,t=3,p=4$placeholder",
		IsAdmin:      true,
	}

	if err := users.Create(ctx, &u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if u.ID == (uuid.UUID{}) {
		t.Error("Create did not assign an id")
	}
	if u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
		t.Error("Create did not assign timestamps")
	}
	// v7 ids carry a timestamp in their high bits; a v4 id here would mean the
	// repository is not using the generator the project settled on.
	if got := u.ID.String()[14]; got != '7' {
		t.Errorf("id %s is version %c, want 7", u.ID, got)
	}

	got, err := users.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Username != u.Username || got.PasswordHash != u.PasswordHash || !got.IsAdmin {
		t.Errorf("GetByID returned %+v, want %+v", got, u)
	}
	// timestamptz round-trips as UTC at microsecond resolution; the repository
	// truncates on write so the value read back is the value written.
	if !got.CreatedAt.Equal(u.CreatedAt) {
		t.Errorf("created_at changed in transit: wrote %s, read %s", u.CreatedAt, got.CreatedAt)
	}
}

func TestUserRepositoryGetByUsernameIsCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	users := repository.NewUserRepository(pool)

	u := domain.User{Username: "Steven", PasswordHash: "x", IsAdmin: true}
	if err := users.Create(ctx, &u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, name := range []string{"Steven", "steven", "STEVEN", "sTeVeN"} {
		got, err := users.GetByUsername(ctx, name)
		if err != nil {
			t.Errorf("GetByUsername(%q): %v", name, err)
			continue
		}
		if got.ID != u.ID {
			t.Errorf("GetByUsername(%q) returned the wrong user", name)
		}
	}

	// The stored spelling is preserved even though matching ignores case.
	got, err := users.GetByUsername(ctx, "steven")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if got.Username != "Steven" {
		t.Errorf("stored username is %q, want %q", got.Username, "Steven")
	}
}

func TestUserRepositoryDuplicateUsernameConflicts(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	users := repository.NewUserRepository(pool)

	first := domain.User{Username: "admin", PasswordHash: "x"}
	if err := users.Create(ctx, &first); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	// Differing only by case must still collide: the unique index is on
	// lower(username).
	second := domain.User{Username: "ADMIN", PasswordHash: "y"}
	err := users.Create(ctx, &second)
	if !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("Create with a duplicate username returned %v, want ErrConflict", err)
	}
}

func TestUserRepositoryNotFound(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	users := repository.NewUserRepository(pool)

	if _, err := users.GetByID(ctx, uuid.NewV7()); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("GetByID for an absent user returned %v, want ErrNotFound", err)
	}
	if _, err := users.GetByUsername(ctx, "nobody"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("GetByUsername for an absent user returned %v, want ErrNotFound", err)
	}
}

func TestUserRepositoryCount(t *testing.T) {
	ctx := context.Background()
	pool := migratedDB(t)
	users := repository.NewUserRepository(pool)

	// First-run detection depends on this being zero on a fresh database.
	n, err := users.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 0 {
		t.Fatalf("fresh database reports %d users, want 0", n)
	}

	for _, name := range []string{"a", "b", "c"} {
		u := domain.User{Username: name, PasswordHash: "x"}
		if err := users.Create(ctx, &u); err != nil {
			t.Fatalf("Create(%q): %v", name, err)
		}
	}

	if n, err = users.Count(ctx); err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 3 {
		t.Errorf("Count returned %d, want 3", n)
	}
}
