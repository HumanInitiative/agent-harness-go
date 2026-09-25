package tools

import (
	"context"
	"testing"
	"time"
)

func TestCurrentTimeTool_Execute_DefaultsToUTC(t *testing.T) {
	got, err := NewCurrentTimeTool().Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	parsed, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("expected an RFC3339 timestamp, got %q: %v", got, err)
	}
	if _, offset := parsed.Zone(); offset != 0 {
		t.Fatalf("expected UTC, got offset %d", offset)
	}
}

func TestCurrentTimeTool_Execute_HonoursTimezone(t *testing.T) {
	got, err := NewCurrentTimeTool().Execute(context.Background(), []byte(`{"timezone": "Asia/Jakarta"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	parsed, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("expected an RFC3339 timestamp, got %q: %v", got, err)
	}
	if _, offset := parsed.Zone(); offset != 7*60*60 {
		t.Fatalf("expected UTC+7 for Asia/Jakarta, got offset %d", offset)
	}
}

func TestCurrentTimeTool_Execute_RejectsUnknownTimezone(t *testing.T) {
	if _, err := NewCurrentTimeTool().Execute(context.Background(), []byte(`{"timezone": "Not/A_Real_Zone"}`)); err == nil {
		t.Fatal("expected error for unknown timezone, got nil")
	}
}
