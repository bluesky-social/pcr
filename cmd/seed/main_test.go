package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sarah/go-prod-change-registry/internal/model"
)

func TestRunSeedsFixtureOverAPI(t *testing.T) {
	t.Parallel()

	var requests []model.CreateChangeRequest
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer demo-token" {
			t.Errorf("Authorization = %q", got)
		}
		var request model.CreateChangeRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return nil, err
		}
		requests = append(requests, request)
		body := fmt.Sprintf(`{"id":"event-%d"}`, len(requests))
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	fixturePath := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(fixturePath, []byte(`{
		"events": [
			{"ref":"parent","external_id":"parent","user_name":"alice","event_type":"deployment","description":"parent"},
			{"ref":"child","parent_ref":"parent","external_id":"child","user_name":"bob","event_type":"alert","description":"child"}
		]
	}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runWithClient([]string{
		"--base-url=http://registry.test",
		"--token=demo-token",
		"--fixture=" + fixturePath,
	}, &stdout, &stderr, client)
	if code != 0 {
		t.Fatalf("run() = %d, stderr = %s", code, stderr.String())
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if requests[1].ParentID != "event-1" {
		t.Errorf("child ParentID = %q, want event-1", requests[1].ParentID)
	}
	if got := stdout.String(); got != "seeded 2 events from "+fixturePath+"\n" {
		t.Errorf("stdout = %q", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
