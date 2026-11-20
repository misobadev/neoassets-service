package apperr

import (
	"errors"
	"fmt"
	"testing"
)

func TestKindOfAndMessage(t *testing.T) {
	err := fmt.Errorf("approve: %w", NotFound("submission not found"))
	if KindOf(err) != KindNotFound {
		t.Fatalf("KindOf = %v", KindOf(err))
	}
	if NotFound("x").Error() != "x" {
		t.Fatal("message changed")
	}
	if KindOf(errors.New("plain")) != 0 {
		t.Fatal("plain error has a kind")
	}
}

func TestIsComparesKindAndMessage(t *testing.T) {
	sentinel := Conflict("submission is not pending")
	if !errors.Is(fmt.Errorf("wrap: %w", Conflict("submission is not pending")), sentinel) {
		t.Fatal("same kind and message should match")
	}
	if errors.Is(Invalid("submission is not pending"), sentinel) {
		t.Fatal("different kind should not match")
	}
}
