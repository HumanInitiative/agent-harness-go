package hiprograms

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

const goodFile = `
programs:
  - id: beasiswa-anak-desa
    name: Beasiswa Anak Desa
    description: Beasiswa untuk 120 anak.
    focus_areas: [pendidikan, " "]
    regions: [Jawa Barat]
    program_types: [beasiswa]
  - id: air-bersih
    name: Air Bersih untuk Dusun
    focus_areas: [air bersih]
`

func TestParse(t *testing.T) {
	c, err := Parse(strings.NewReader(goodFile))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := c.Programs(context.Background())
	if len(got) != 2 || got[0].ID != "beasiswa-anak-desa" || got[1].ID != "air-bersih" {
		t.Fatalf("programs = %+v (file order must be kept)", got)
	}
	if len(got[0].FocusAreas) != 1 || got[0].Regions[0] != "Jawa Barat" || got[0].ProgramTypes[0] != "beasiswa" {
		t.Errorf("fields: %+v", got[0])
	}
	// What the caller gets is a copy.
	got[0].Name = "diubah"
	if again, _ := c.Programs(context.Background()); again[0].Name != "Beasiswa Anak Desa" {
		t.Error("Programs must return a copy")
	}

	p, err := c.Program(context.Background(), "air-bersih")
	if err != nil || p.Name != "Air Bersih untuk Dusun" {
		t.Fatalf("Program = %+v, %v", p, err)
	}
	if _, err := c.Program(context.Background(), "tidak-ada"); !errors.Is(err, outbound.ErrProgramNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

func TestParse_RefusesAMistakeInTheFile(t *testing.T) {
	cases := map[string]string{
		"no id":         "programs:\n  - name: A\n    focus_areas: [x]\n",
		"bad id":        "programs:\n  - id: Beasiswa Desa\n    name: A\n    focus_areas: [x]\n",
		"duplicate id":  "programs:\n  - {id: a, name: A, focus_areas: [x]}\n  - {id: a, name: B, focus_areas: [x]}\n",
		"no focus area": "programs:\n  - id: a\n    name: A\n",
		"no name":       "programs:\n  - id: a\n    focus_areas: [x]\n",
		"unknown field": "programs:\n  - id: a\n    name: A\n    focus_areas: [x]\n    budget: 5\n",
		"not yaml":      "programs: [",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(doc)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hi-programs.yaml")
	if err := os.WriteFile(path, []byte(goodFile), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := Load(path); err != nil {
		t.Fatal(err)
	} else if got, _ := c.Programs(context.Background()); len(got) != 2 {
		t.Errorf("programs = %d", len(got))
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("a missing file must be an error")
	}
	if c, err := Parse(strings.NewReader("programs: []\n")); err != nil {
		t.Fatal(err)
	} else if got, _ := c.Programs(context.Background()); len(got) != 0 {
		t.Error("an empty catalog is allowed")
	}
}
