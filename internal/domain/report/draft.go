package report

import (
	"errors"
	"fmt"
	"strings"
)

// Section names a narrative slot in the report template. The figure
// sections (summary, funds received, funds disbursed, beneficiaries) have
// no slot: code fills them.
type Section string

const (
	// SectionActivities is "Activities and impact".
	SectionActivities Section = "activities"
	// SectionChallenges is "Challenges and next steps".
	SectionChallenges Section = "challenges"
)

// NarrativeSections lists the narrative slots in template order.
func NarrativeSections() []Section { return []Section{SectionActivities, SectionChallenges} }

var sectionTitles = map[Section]string{
	SectionActivities: "Activities and impact",
	SectionChallenges: "Challenges and next steps",
}

// Narrative is the text for one slot, with the documents it was written
// from. A narrative that cites nothing cannot be checked by a reviewer, so
// Assemble refuses it.
type Narrative struct {
	Section Section
	Text    string
	Sources []string
}

// BlockKind says how a Block is laid out.
type BlockKind int

const (
	// BlockHeading is a heading of the given Level (1 is the title).
	BlockHeading BlockKind = iota
	// BlockParagraph is a paragraph of Text, bold when Bold is set.
	BlockParagraph
	// BlockTable is a table with a Header row. The first column is text and
	// the others are numbers (right aligned). When TotalRow is set the last
	// row is a total and is shown in bold.
	BlockTable
	// BlockBullets is a bulleted list of Items.
	BlockBullets
)

// Block is one element of a Draft.
type Block struct {
	Kind     BlockKind
	Level    int
	Text     string
	Bold     bool
	Header   []string
	Rows     [][]string
	TotalRow bool
	Items    []string
}

// Draft is a finished report draft, independent of file format. Renderers
// (Markdown here, DOCX in an adapter) turn the same blocks into a file.
type Draft struct {
	Blocks []Block
	// Sources are the documents cited by the narratives, in order of first
	// use.
	Sources []string
	// Notes are things the reviewer must look at, found by the checks run
	// before assembly (for example a number the documents do not contain).
	// They are also part of the Blocks.
	Notes []string
}

// Assemble fills the template with the figures and the narratives. It
// fails, instead of leaving a gap, if a slot is missing, empty, uncited, or
// unknown, so an incomplete draft never looks complete. reviewNotes are
// shown to the reviewer in the draft.
func Assemble(f Figures, narratives []Narrative, reviewNotes []string) (Draft, error) {
	if err := f.Validate(); err != nil {
		return Draft{}, fmt.Errorf("assemble report: %w", err)
	}
	bySection, err := indexNarratives(narratives)
	if err != nil {
		return Draft{}, fmt.Errorf("assemble report: %w", err)
	}

	d := Draft{Notes: append([]string(nil), reviewNotes...)}
	add := func(b ...Block) { d.Blocks = append(d.Blocks, b...) }

	add(
		Block{Kind: BlockHeading, Level: 1, Text: "Donation report: " + f.Program.Name},
		Block{Kind: BlockParagraph, Text: "Period: " + formatDate(f.Period.From) + " to " + formatDate(f.Period.To)},
		Block{Kind: BlockParagraph, Bold: true, Text: "DRAFT. Written with AI assistance from the donation data and " +
			"activity reports. Review before sending."},
		Block{Kind: BlockParagraph, Text: "Reviewed by: ____________  Date: ____________"},
	)
	if len(d.Notes) > 0 {
		add(
			Block{Kind: BlockHeading, Level: 2, Text: "Notes for the reviewer"},
			Block{Kind: BlockBullets, Items: d.Notes},
		)
	}

	add(
		Block{Kind: BlockHeading, Level: 2, Text: "1. Summary"},
		Block{Kind: BlockTable, Header: []string{"Measure", "Value"}, Rows: [][]string{
			{"Funds received", FormatRupiah(f.TotalReceived())},
			{"Funds disbursed", FormatRupiah(f.TotalDisbursed())},
			{"Balance", FormatRupiah(f.Balance())},
			{"Beneficiaries", formatInt(int64(f.TotalBeneficiaries()))},
		}},
	)
	addAmounts(&d, "2. Funds received", "Month or source", f.Received, f.TotalReceived())
	addAmounts(&d, "3. Funds disbursed", "Category", f.Disbursed, f.TotalDisbursed())

	add(Block{Kind: BlockHeading, Level: 2, Text: "4. Beneficiaries"})
	if len(f.Beneficiaries) == 0 {
		add(Block{Kind: BlockParagraph, Text: "No beneficiaries recorded for this period."})
	} else {
		rows := make([][]string, 0, len(f.Beneficiaries)+1)
		for _, c := range f.Beneficiaries {
			rows = append(rows, []string{c.Label, formatInt(int64(c.People))})
		}
		rows = append(rows, []string{"Total", formatInt(int64(f.TotalBeneficiaries()))})
		add(Block{Kind: BlockTable, Header: []string{"Group", "People"}, Rows: rows, TotalRow: true})
	}

	seen := map[string]bool{}
	for i, section := range NarrativeSections() {
		n := bySection[section]
		sources := cleanSources(n.Sources)
		add(
			Block{Kind: BlockHeading, Level: 2, Text: fmt.Sprintf("%d. %s", 5+i, sectionTitles[section])},
			Block{Kind: BlockParagraph, Text: strings.TrimSpace(n.Text)},
			Block{Kind: BlockParagraph, Text: "Sources: " + strings.Join(sources, "; ")},
		)
		for _, s := range sources {
			if !seen[s] {
				seen[s] = true
				d.Sources = append(d.Sources, s)
			}
		}
	}

	add(
		Block{Kind: BlockHeading, Level: 2, Text: "Appendix"},
		Block{Kind: BlockParagraph, Text: "Data read on " + formatDate(f.AsOf) + "."},
		Block{Kind: BlockParagraph, Text: "Source documents:"},
		Block{Kind: BlockBullets, Items: d.Sources},
	)
	return d, nil
}

func indexNarratives(narratives []Narrative) (map[Section]Narrative, error) {
	by := map[Section]Narrative{}
	var problems []string
	for _, n := range narratives {
		if _, ok := sectionTitles[n.Section]; !ok {
			problems = append(problems, fmt.Sprintf("unknown section %q", n.Section))
			continue
		}
		if _, dup := by[n.Section]; dup {
			problems = append(problems, fmt.Sprintf("section %q given twice", n.Section))
			continue
		}
		by[n.Section] = n
	}
	for _, section := range NarrativeSections() {
		n, ok := by[section]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("section %q is missing", section))
		case strings.TrimSpace(n.Text) == "":
			problems = append(problems, fmt.Sprintf("section %q is empty", section))
		case len(cleanSources(n.Sources)) == 0:
			problems = append(problems, fmt.Sprintf("section %q cites no source document", section))
		}
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}
	return by, nil
}

func cleanSources(sources []string) []string {
	var out []string
	for _, s := range sources {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func addAmounts(d *Draft, heading, labelHeader string, lines []Amount, total int64) {
	d.Blocks = append(d.Blocks, Block{Kind: BlockHeading, Level: 2, Text: heading})
	if len(lines) == 0 {
		d.Blocks = append(d.Blocks, Block{Kind: BlockParagraph, Text: "Nothing recorded for this period."})
		return
	}
	rows := make([][]string, 0, len(lines)+1)
	for _, a := range lines {
		rows = append(rows, []string{a.Label, FormatRupiah(a.Rupiah)})
	}
	rows = append(rows, []string{"Total", FormatRupiah(total)})
	d.Blocks = append(d.Blocks, Block{Kind: BlockTable, Header: []string{labelHeader, "Amount"}, Rows: rows, TotalRow: true})
}
