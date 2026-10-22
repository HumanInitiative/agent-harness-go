package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"

	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/csrprospects"
	"github.com/HumanInitiative/agent-harness-go/internal/bootstrap"
	"github.com/HumanInitiative/agent-harness-go/internal/domain/outreach"
	"github.com/HumanInitiative/agent-harness-go/internal/platform/config"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch/csr"
)

// defaultMinConfidence is used when there is no institution profile file to
// take the extraction-confidence threshold from.
const defaultMinConfidence = 0.6

// loadProgram reads a program file:
//
//	program:
//	  name: Beasiswa Anak Desa
//	  description: Beasiswa untuk 120 anak di desa terpencil Jawa Barat.
//	  focus_areas: [pendidikan]
//	  regions: [Jawa Barat]
//	  program_types: [beasiswa]
func loadProgram(path string) (outreach.Program, error) {
	f, err := os.Open(path)
	if err != nil {
		return outreach.Program{}, err
	}
	defer f.Close()
	var doc struct {
		Program struct {
			Name         string   `yaml:"name"`
			Description  string   `yaml:"description"`
			FocusAreas   []string `yaml:"focus_areas"`
			Regions      []string `yaml:"regions"`
			ProgramTypes []string `yaml:"program_types"`
		} `yaml:"program"`
	}
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return outreach.Program{}, fmt.Errorf("read program %s: %w", path, err)
	}
	p := outreach.Program{
		Name: doc.Program.Name, Description: doc.Program.Description, FocusAreas: doc.Program.FocusAreas,
		Regions: doc.Program.Regions, ProgramTypes: doc.Program.ProgramTypes,
	}.Clean()
	if err := p.Validate(); err != nil {
		return outreach.Program{}, fmt.Errorf("program %s: %w", path, err)
	}
	return p, nil
}

// cmdShortlist ranks the indexed companies against one program. It reads
// only the index, so it needs no model and costs nothing: use it to see what
// a shortlist would be, to tune scoring, and (with -csv) to hand a person a
// sheet to rate how relevant each company is.
func cmdShortlist(ctx context.Context, cfg config.CrawlerConfig, store *csr.Store, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("shortlist", flag.ContinueOnError)
	file := fs.String("program", "", "YAML file describing the program (required)")
	limit := fs.Int("limit", 10, "number of companies")
	asCSV := fs.Bool("csv", false, "write a CSV rating sheet instead of a table")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("usage: csrctl shortlist -program FILE.yaml [-limit N] [-csv]")
	}
	if *limit < 1 {
		return errors.New("-limit must be at least 1")
	}
	program, err := loadProgram(*file)
	if err != nil {
		return err
	}
	minConfidence := defaultMinConfidence
	if ip, err := bootstrap.LoadInstitutionProfile(cfg.CSR); err == nil && ip.MinConfidence > 0 {
		minConfidence = ip.MinConfidence
	}
	source := csrprospects.New(store, minConfidence, csr.IndexOptions{StaleAfter: cfg.CSR.StaleAfter})
	found, err := source.Prospects(ctx, program, *limit)
	if err != nil {
		return err
	}
	warnings := outreach.Warnings(found.Terms, source.FocusGroups())
	if *asCSV {
		// The sheet goes to stdout; what is wrong with the criteria must not
		// end up inside it.
		writeWarnings(os.Stderr, warnings)
		if len(found.Candidates) == 0 {
			return errors.New("no indexed company fits this program: none has a matching focus area")
		}
		return writeRatingSheet(out, found.Candidates)
	}
	writeWarnings(out, warnings)
	if len(found.Candidates) == 0 {
		fmt.Fprintln(out, "No indexed company fits this program: none has a matching focus area. Is the index crawled?")
		return nil
	}

	fmt.Fprintf(out, "%s: %d of %d fitting companies\n\n", program.Name, len(found.Candidates), found.Fitting)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "#\tID\tCOMPANY\tSCORE\tREVIEW\tDATA\tPROPOSAL CHANNEL\tTOP REASON")
	for i, c := range found.Candidates {
		fmt.Fprintf(w, "%d\t%d\t%s\t%d\t%s\t%s\t%s\t%s\n", i+1, c.CompanyID, c.Name, c.Score, reviewLabel(c), dataLabel(c), dash(c.ProposalChannel), firstReason(c))
	}
	return w.Flush()
}

func writeWarnings(w io.Writer, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintln(w, "Check the program's criteria:")
	for _, msg := range warnings {
		fmt.Fprintln(w, "  - "+msg)
	}
	fmt.Fprintln(w)
}

func writeRatingSheet(out io.Writer, candidates []outreach.Candidate) error {
	w := csv.NewWriter(out)
	_ = w.Write([]string{"rank", "company_id", "company", "score", "review_status", "data_confirmed", "proposal_channel", "reasons", "programs", "relevant (y/n)", "comment"})
	for i, c := range candidates {
		var progs []string
		for _, p := range c.Programs {
			progs = append(progs, p.Name)
		}
		_ = w.Write([]string{
			fmt.Sprint(i + 1), fmt.Sprint(c.CompanyID), c.Name, fmt.Sprint(c.Score), reviewLabel(c), dataLabel(c),
			c.ProposalChannel, strings.Join(c.Reasons, " | "), strings.Join(progs, " | "), "", "",
		})
	}
	w.Flush()
	return w.Error()
}

func reviewLabel(c outreach.Candidate) string {
	if c.Verified {
		return "verified"
	}
	return "new"
}

func dataLabel(c outreach.Candidate) string {
	if c.ConfirmedAt.IsZero() {
		return "unknown"
	}
	label := c.ConfirmedAt.Format("2006-01-02")
	if c.Stale {
		label += " (stale)"
	}
	return label
}

func firstReason(c outreach.Candidate) string {
	if len(c.Reasons) == 0 {
		return "-"
	}
	return c.Reasons[0]
}
