package handler //nolint:testpackage // fuzzing the unexported form parser is intentional

import (
	"net/url"
	"strings"
	"testing"
)

func FuzzParseLinkForm(f *testing.F) {
	for _, seed := range []string{
		"link_label=Incident&link_url=https%3A%2F%2Fexample.pagerduty.com%2Fincidents%2FP1",
		"link_label=Plan&link_label=PR&link_url=https%3A%2F%2Fnotion.so%2Fplan&link_url=https%3A%2F%2Fgithub.com%2Forg%2Frepo%2Fpull%2F1",
		"link_label=&link_url=",
		"link_url=%zz",
		strings.Repeat("link_url=https%3A%2F%2Fexample.com%26", 300),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		form, err := url.ParseQuery(raw)
		if err != nil {
			return
		}
		links := parseLinkForm(form)
		if len(links) > len(form["link_url"]) {
			t.Fatalf("parsed %d links from %d URL fields", len(links), len(form["link_url"]))
		}
		for _, link := range links {
			if link.Label != strings.TrimSpace(link.Label) || link.URL != strings.TrimSpace(link.URL) {
				t.Fatalf("untrimmed link: %#v", link)
			}
			if link.Label == "" && link.URL == "" {
				t.Fatal("empty link was retained")
			}
		}
	})
}
