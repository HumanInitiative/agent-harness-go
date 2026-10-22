// Package hiprograms implements outbound.OutreachPrograms over a YAML file
// listing Human Initiative's programs, so a person picks a program instead of
// retyping its criteria:
//
//	programs:
//	  - id: beasiswa-anak-desa
//	    name: Beasiswa Anak Desa
//	    description: Beasiswa untuk 120 anak di desa terpencil Jawa Barat.
//	    focus_areas: [pendidikan]
//	    regions: [Jawa Barat]
//	    program_types: [beasiswa]
//
// The file is read once, at startup, and every program in it is validated
// then: a mistake in the file stops the harness from starting instead of
// surfacing as an odd shortlist later.
package hiprograms

import (
	"context"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/outreach"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

// Catalog holds the programs read from a file.
type Catalog struct {
	programs []outreach.Program
}

// Load reads and validates the catalog at path.
func Load(path string) (*Catalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("hiprograms: %w", err)
	}
	defer f.Close()
	c, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("hiprograms: %s: %w", path, err)
	}
	return c, nil
}

// Parse reads a catalog from r.
func Parse(r io.Reader) (*Catalog, error) {
	var doc struct {
		Programs []struct {
			ID           string   `yaml:"id"`
			Name         string   `yaml:"name"`
			Description  string   `yaml:"description"`
			FocusAreas   []string `yaml:"focus_areas"`
			Regions      []string `yaml:"regions"`
			ProgramTypes []string `yaml:"program_types"`
		} `yaml:"programs"`
	}
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("read programs: %w", err)
	}
	c := &Catalog{}
	seen := map[string]bool{}
	for i, p := range doc.Programs {
		prog := outreach.Program{
			ID: p.ID, Name: p.Name, Description: p.Description,
			FocusAreas: p.FocusAreas, Regions: p.Regions, ProgramTypes: p.ProgramTypes,
		}.Clean()
		if !outreach.ValidProgramID(prog.ID) {
			return nil, fmt.Errorf("program %d: id %q must be lowercase letters, digits and dashes (at most 64 characters)", i+1, prog.ID)
		}
		if seen[prog.ID] {
			return nil, fmt.Errorf("program %d: id %q is used twice", i+1, prog.ID)
		}
		seen[prog.ID] = true
		if err := prog.Validate(); err != nil {
			return nil, fmt.Errorf("program %q: %w", prog.ID, err)
		}
		c.programs = append(c.programs, prog)
	}
	return c, nil
}

// Programs implements outbound.OutreachPrograms.
func (c *Catalog) Programs(context.Context) ([]outreach.Program, error) {
	return append([]outreach.Program(nil), c.programs...), nil
}

// Program implements outbound.OutreachPrograms.
func (c *Catalog) Program(_ context.Context, id string) (outreach.Program, error) {
	for _, p := range c.programs {
		if p.ID == id {
			return p, nil
		}
	}
	return outreach.Program{}, outbound.ErrProgramNotFound
}
