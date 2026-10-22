package outreach

import (
	"fmt"
	"strings"
)

// Warnings explains the terms of a program that will not work as the person
// who wrote them probably hopes: a focus area or program type the scoring
// does not know (only exact words match), and any term no indexed company
// matches. groups are the fields the scoring does know, named in the advice.
func Warnings(terms []TermCoverage, groups []FocusGroup) []string {
	var known []string
	for _, g := range groups {
		known = append(known, g.Name)
	}
	var out []string
	for _, t := range terms {
		label := termLabel(t.Kind)
		switch {
		case !t.Recognized:
			out = append(out, fmt.Sprintf(
				"%s \"%s\" bukan salah satu yang dikenali sistem (%s). Ia hanya cocok dengan perusahaan yang memakai kata yang sama persis: %s. "+
					"Periksa ejaannya, atau pakai istilah yang dikenali.",
				label, t.Term, strings.Join(known, ", "), companiesPhrase(t.Companies)))
		case t.Companies == 0:
			out = append(out, fmt.Sprintf("Belum ada perusahaan di indeks yang %s \"%s\"%s.", termVerb(t.Kind), t.Term, nationwideNote(t.Kind)))
		}
	}
	return out
}

func termLabel(kind string) string {
	switch kind {
	case TermRegion:
		return "Wilayah"
	case TermType:
		return "Jenis dukungan"
	}
	return "Bidang"
}

func termVerb(kind string) string {
	switch kind {
	case TermRegion:
		return "menyebut wilayah"
	case TermType:
		return "menjalankan program jenis"
	}
	return "bekerja di bidang"
}

func nationwideNote(kind string) string {
	if kind == TermRegion {
		return " atau berskala nasional"
	}
	return ""
}

func companiesPhrase(n int) string {
	if n == 0 {
		return "belum ada perusahaan di indeks yang memakainya"
	}
	return fmt.Sprintf("%d perusahaan di indeks memakainya", n)
}
