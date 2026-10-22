package report

import "strings"

// Markdown renders the draft as Markdown.
func (d Draft) Markdown() string {
	var b strings.Builder
	for _, blk := range d.Blocks {
		switch blk.Kind {
		case BlockHeading:
			b.WriteString(strings.Repeat("#", max(blk.Level, 1)) + " " + blk.Text + "\n\n")
		case BlockParagraph:
			if blk.Bold {
				b.WriteString("**" + blk.Text + "**\n\n")
			} else {
				b.WriteString(blk.Text + "\n\n")
			}
		case BlockTable:
			writeMarkdownTable(&b, blk)
		case BlockBullets:
			for _, item := range blk.Items {
				b.WriteString("- " + item + "\n")
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func writeMarkdownTable(b *strings.Builder, blk Block) {
	b.WriteString("| " + strings.Join(blk.Header, " | ") + " |\n")
	b.WriteString("|---")
	for range blk.Header[1:] {
		b.WriteString("|---:")
	}
	b.WriteString("|\n")
	for i, row := range blk.Rows {
		cells := row
		if blk.TotalRow && i == len(blk.Rows)-1 {
			cells = make([]string, len(row))
			for j, c := range row {
				cells[j] = "**" + c + "**"
			}
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	b.WriteString("\n")
}
