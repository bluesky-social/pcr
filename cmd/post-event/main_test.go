package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRunPostsEventWithoutCredentialOrUserNameLeak(t *testing.T) {
	t.Parallel()

	const credential = "alice@example.com:secret-app-password"
	var gotAuth string
	var gotPayload map[string]any
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"id":"event-1","external_id":"build-123","user_name":"alice@example.com","user_provider":"beyond","user_subject":"alice@example.com","event_type":"deployment","description":"deployed PCR"}`,
			)),
			Request: r,
		}, nil
	})}

	getenv := func(name string) string {
		switch name {
		case "PCR_CREDENTIAL":
			return credential
		default:
			return ""
		}
	}
	var stdout, stderr strings.Builder
	code := runWithClient(context.Background(), []string{
		"--base-url", "https://pcr.example.com",
		"--external-id", "build-123",
		"--description", "deployed PCR",
		"--tag", "env=prod",
	}, getenv, &stdout, &stderr, client)

	if code != 0 {
		t.Fatalf("run() = %d, stderr = %q", code, stderr.String())
	}
	if gotAuth != "Bearer "+credential {
		t.Errorf("Authorization header was not the configured credential")
	}
	if _, exists := gotPayload["user_name"]; exists {
		t.Errorf("request payload contains caller-supplied user_name: %#v", gotPayload)
	}
	if tags, ok := gotPayload["tags"].(map[string]any); !ok || tags["env"] != "prod" {
		t.Errorf("request tags = %#v, want env=prod", gotPayload["tags"])
	}
	if strings.Contains(stdout.String(), credential) || strings.Contains(stderr.String(), credential) {
		t.Fatal("credential leaked to command output")
	}
	if !strings.Contains(stdout.String(), `"user_provider":"beyond"`) {
		t.Errorf("stdout = %q, want resulting event", stdout.String())
	}
}

func TestHTTPClientRejectsRedirect(t *testing.T) {
	t.Parallel()

	err := newHTTPClient().CheckRedirect(&http.Request{}, []*http.Request{{}})
	if !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect() error = %v, want http.ErrUseLastResponse", err)
	}
}

func TestRunRequiresCredentialWithoutPrintingIt(t *testing.T) {
	t.Parallel()

	var stdout, stderr strings.Builder
	code := run(context.Background(), []string{
		"--external-id", "build-123",
		"--description", "deployed PCR",
	}, func(string) string { return "" }, &stdout, &stderr)

	if code != 2 || !strings.Contains(stderr.String(), "PCR_CREDENTIAL is required") {
		t.Fatalf("run() = %d, stderr = %q", code, stderr.String())
	}
}
