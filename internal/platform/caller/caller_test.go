package caller

import (
	"context"
	"testing"
)

func TestKeyID(t *testing.T) {
	if KeyID(context.Background()) != "" {
		t.Fatal("an unauthenticated context has no key")
	}
	if got := KeyID(WithKeyID(context.Background(), "a1b2c3d4e5f6")); got != "a1b2c3d4e5f6" {
		t.Fatalf("got %q", got)
	}
}
