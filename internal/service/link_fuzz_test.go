package service //nolint:testpackage // fuzzing the unexported security validator is intentional

import (
	"net/url"
	"strings"
	"testing"

	"github.com/sarah/go-prod-change-registry/internal/model"
)

func FuzzValidateLinks(f *testing.F) {
	for _, seed := range []struct{ label, linkURL string }{
		{"Incident", "https://example.pagerduty.com/incidents/P1"},
		{"PR", "https://github.com/org/repo/pull/1#discussion_r2"},
		{"unsafe", "javascript:alert(1)"},
		{"deceptive", "https://trusted.example@evil.example/path"},
		{"control", "https://example.com/%0d%0aheader"},
	} {
		f.Add(seed.label, seed.linkURL)
	}

	f.Fuzz(func(t *testing.T, label, linkURL string) {
		link := model.EventLink{Label: label, URL: linkURL}
		if validateLinks([]model.EventLink{link}) != nil {
			return
		}
		parsed, err := url.ParseRequestURI(linkURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
			t.Fatalf("unsafe URL accepted: %q", linkURL)
		}
		unescaped, err := url.PathUnescape(linkURL)
		if err != nil || strings.Contains(linkURL, "\\") || strings.Contains(unescaped, "\\") || strings.IndexFunc(unescaped, isUnsafeLinkRune) >= 0 {
			t.Fatalf("ambiguous URL accepted: %q", linkURL)
		}
		if len(label) > maxLinkLabelBytes || len(linkURL) > maxLinkURLBytes || strings.IndexFunc(label, isUnsafeLinkRune) >= 0 {
			t.Fatalf("oversized or control-bearing link accepted")
		}
	})
}
