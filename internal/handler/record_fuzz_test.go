package handler //nolint:testpackage // fuzzing the unexported form parsers is intentional

import (
	"maps"
	"strings"
	"testing"
)

func FuzzParseRecordChangeTags(f *testing.F) {
	for _, seed := range []string{
		"",
		"team=payments\nscope=service\nseverity=sev2",
		" change_id = deploy-123 \n phase = start ",
		"key=value=with=equals",
		"duplicate=one\nduplicate=two",
		"missing-separator",
		"=missing-key",
		"emoji=🚀\nregion=us-west-2",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		tags, message := parseRecordChangeTags(raw)
		repeatedTags, repeatedMessage := parseRecordChangeTags(raw)
		if message != repeatedMessage || !maps.Equal(tags, repeatedTags) {
			t.Fatalf("parseRecordChangeTags(%q) is not deterministic", raw)
		}
		if message != "" {
			return
		}

		var canonical strings.Builder
		for key, value := range tags {
			if key == "" || key != strings.TrimSpace(key) || value != strings.TrimSpace(value) {
				t.Fatalf("parseRecordChangeTags(%q) returned untrimmed tag %q=%q", raw, key, value)
			}
			canonical.WriteString(key)
			canonical.WriteByte('=')
			canonical.WriteString(value)
			canonical.WriteByte('\n')
		}
		roundTrip, roundTripMessage := parseRecordChangeTags(canonical.String())
		if roundTripMessage != "" || !maps.Equal(tags, roundTrip) {
			t.Fatalf("parseRecordChangeTags(%q) did not round trip: tags=%v, roundTrip=%v, message=%q", raw, tags, roundTrip, roundTripMessage)
		}
	})
}
