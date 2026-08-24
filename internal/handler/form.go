package handler

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/sarah/go-prod-change-registry/internal/model"
)

// maxFormBytes bounds POST form bodies handled by this package. The
// dashboard and /login endpoints accept only short fields (auth tokens,
// CSRF tokens), so 8 KiB is generous. The bound exists to prevent
// unbounded memory consumption from a crafted request body — see gosec
// G120 (CWE-409).
const maxFormBytes = 8 << 10

// parseBoundedPostForm wraps r.Body in http.MaxBytesReader and calls
// ParseForm. It writes 413 on a body-too-large error and 400 on any other
// parse failure, then returns false so the caller can return immediately.
// On success the caller may read values via r.PostFormValue / r.FormValue.
func parseBoundedPostForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return false
	}
	return true
}

func parseLinkForm(form url.Values) []model.EventLink {
	labels := form["link_label"]
	urls := form["link_url"]
	links := make([]model.EventLink, 0, len(urls))
	for i, rawURL := range urls {
		linkURL := strings.TrimSpace(rawURL)
		label := ""
		if i < len(labels) {
			label = strings.TrimSpace(labels[i])
		}
		if linkURL == "" && label == "" {
			continue
		}
		links = append(links, model.EventLink{Label: label, URL: linkURL})
	}
	return links
}
