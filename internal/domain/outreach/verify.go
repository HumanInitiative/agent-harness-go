package outreach

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/report"
)

const maxEmailChars = 3000

var (
	emailAddress = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	webAddress   = regexp.MustCompile(`https?://[^\s)>\]"']+`)
)

// VerifyEmail checks a drafted email against the candidate's evidence and
// the program it asks for. Problems are what make the email unusable (the
// model is asked again); notes are things a person should look at in an
// otherwise usable email.
func VerifyEmail(e Email, c Candidate, p Program) (problems, notes []string) {
	if strings.TrimSpace(e.Subject) == "" {
		problems = append(problems, "the subject is empty")
	}
	if strings.TrimSpace(e.Body) == "" {
		problems = append(problems, "the body is empty")
	}
	if len([]rune(e.Body)) > maxEmailChars {
		problems = append(problems, fmt.Sprintf("the body is longer than %d characters; keep it short", maxEmailChars))
	}
	if len(e.Cited) == 0 {
		problems = append(problems, "no evidence is cited: cite the evidence ids every claim about the company rests on")
	}
	var corpus strings.Builder
	corpus.WriteString(p.Name + " " + p.Description + " " + c.ProposalChannel + " " + c.Domain + " " + c.Name + " ")
	for _, id := range e.Cited {
		ev, ok := c.EvidenceByID(id)
		if !ok {
			problems = append(problems, fmt.Sprintf("evidence %d is not one of the evidence given for this company", id))
			continue
		}
		corpus.WriteString(ev.Title + " " + ev.Excerpt + " " + ev.URL + " ")
	}
	// The email is asked to say when the information is from, so the dates
	// of the evidence and of the last confirmation are legitimate numbers.
	for _, ev := range c.Evidence {
		corpus.WriteString(ev.Date.Format("2006-01-02") + " ")
	}
	corpus.WriteString(c.ConfirmedAt.Format("2006-01-02") + " ")
	for _, prog := range c.Programs {
		corpus.WriteString(prog.Name + " " + prog.PeriodStart + " " + prog.PeriodEnd + " " + prog.ProposalDeadline + " ")
	}

	// A contact address or link that appears nowhere in what the model was
	// given is invented, and an invented address could send a proposal to a
	// stranger.
	known := strings.ToLower(corpus.String())
	for _, found := range append(emailAddress.FindAllString(e.Subject+" "+e.Body, -1), webAddress.FindAllString(e.Subject+" "+e.Body, -1)...) {
		if !strings.Contains(known, strings.ToLower(strings.TrimRight(found, ".,;"))) {
			problems = append(problems, fmt.Sprintf("%q appears in no evidence or program text; never invent contact details", found))
		}
	}

	if bad := report.UnsupportedNumbers(e.Subject+" "+e.Body, report.NumbersIn(corpus.String())); len(bad) > 0 {
		notes = append(notes, "Angka di email ini tidak ada di bukti yang dikutip maupun di data program: "+
			strings.Join(bad, ", ")+". Periksa sebelum dikirim.")
	}
	return problems, notes
}
