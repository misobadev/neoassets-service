package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"neoassets/internal/models"
	"neoassets/internal/testdb"
)

func TestWithTxCommitsOrRollsBack(t *testing.T) {
	r := NewRepository(testdb.Open(t, "repository_review"))
	ctx := context.Background()
	newUser := func(tx *Repository) uuid.UUID {
		u, err := tx.CreateUser(&models.User{
			Username:     "tx" + uuid.NewString()[:8],
			Email:        uuid.NewString()[:8] + "@example.com",
			PasswordHash: "hash",
		})
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}

	var rolledBack uuid.UUID
	boom := errors.New("boom")
	if err := r.WithTx(ctx, func(tx *Repository) error {
		rolledBack = newUser(tx)
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("got %v, want boom", err)
	}
	if _, err := r.GetUserByID(rolledBack); err == nil {
		t.Fatal("a write inside a failed transaction was kept")
	}

	var committed uuid.UUID
	if err := r.WithTx(ctx, func(tx *Repository) error {
		// A nested WithTx joins the outer transaction.
		return tx.WithTx(ctx, func(inner *Repository) error {
			if inner != tx {
				t.Error("nested WithTx opened a second transaction")
			}
			committed = newUser(inner)
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetUserByID(committed); err != nil {
		t.Fatalf("committed write missing: %v", err)
	}
}
