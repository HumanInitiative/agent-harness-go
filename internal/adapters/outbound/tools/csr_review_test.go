package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/platform/caller"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch/csr"
)

var (
	_ outbound.ToolHandler = (*ListNewCompaniesTool)(nil)
	_ outbound.ToolHandler = (*SetCompanyStatusTool)(nil)
)

func reviewStore(t *testing.T) (*csr.Store, int64) {
	t.Helper()
	s, err := csr.OpenStore(context.Background(), ":memory:", func() time.Time { return fixedNow() })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	id, _, err := s.UpsertCompany(context.Background(), csr.Company{Name: "PT Contoh Tambang Tbk", Source: csr.SourceSignal})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"https://berita-a.example/csr", "https://berita-b.example/csr"} {
		if _, err := s.AddSignal(context.Background(), csr.Signal{CompanyID: id, URL: u, Host: strings.Split(u, "/")[2],
			Excerpt: "PT Contoh Tambang Tbk menyalurkan beasiswa di Banten."}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetReviewNote(context.Background(), id, "nama mirip dengan PT Contoh Tambang Lama (id 9)"); err != nil {
		t.Fatal(err)
	}
	return s, id
}

func TestListNewCompanies(t *testing.T) {
	s, _ := reviewStore(t)
	out, err := NewListNewCompaniesTool(s, fixedNow).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"1. PT Contoh Tambang Tbk (id 1) — asal: ditemukan lewat pemberitaan/halaman pihak lain, disebut di 2 situs (2 sinyal)",
		"Catatan untuk ditinjau: nama mirip dengan PT Contoh Tambang Lama (id 9)",
		"https://berita-",
		`"PT Contoh Tambang Tbk menyalurkan beasiswa di Banten."`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

func TestSetCompanyStatus_OnlyReviewerKeysAndAudited(t *testing.T) {
	s, id := reviewStore(t)
	var logs bytes.Buffer
	tool := NewSetCompanyStatusTool(s, []string{"aaaaaaaaaaaa"}, slog.New(slog.NewJSONHandler(&logs, nil)))
	args := json.RawMessage(`{"company_id": 1, "status": "verified"}`)

	for name, ctx := range map[string]context.Context{
		"unauthenticated": context.Background(),
		"other key":       caller.WithKeyID(context.Background(), "bbbbbbbbbbbb"),
	} {
		if _, err := tool.Execute(ctx, args); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("%s: expected a refusal, got %v", name, err)
		}
	}
	if c, _ := s.Company(context.Background(), id); c.Status != csr.StatusNew {
		t.Fatalf("a refused call must change nothing: %s", c.Status)
	}

	out, err := tool.Execute(caller.WithKeyID(context.Background(), "aaaaaaaaaaaa"), args)
	if err != nil || !strings.Contains(out, "changed from new to verified") {
		t.Fatalf("reviewer: %q, %v", out, err)
	}
	if c, _ := s.Company(context.Background(), id); c.Status != csr.StatusVerified {
		t.Fatalf("status not changed: %s", c.Status)
	}
	if !strings.Contains(logs.String(), `"reviewer_key_id":"aaaaaaaaaaaa"`) || !strings.Contains(logs.String(), `"to":"verified"`) {
		t.Fatalf("every change must be logged with its reviewer:\n%s", logs.String())
	}
	if _, err := tool.Execute(caller.WithKeyID(context.Background(), "aaaaaaaaaaaa"), json.RawMessage(`{"company_id": 99, "status": "excluded"}`)); err == nil {
		t.Fatal("an unknown company must be reported")
	}
	if !strings.Contains(tool.Description(), "explicitly asks in their own message") {
		t.Fatal("the description must keep the model from acting on content")
	}
}
