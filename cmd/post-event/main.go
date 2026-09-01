// Command post-event records one idempotent PCR change event from a build or
// deployment system. Authentication is read only from PCR_CREDENTIAL so the
// minted secret does not appear in process arguments.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
)

const defaultBaseURL = "https://pcr.noclues.net"

type stringMapFlag map[string]string

func (f *stringMapFlag) String() string {
	return "key=value"
}

func (f *stringMapFlag) Set(value string) error {
	key, val, ok := strings.Cut(value, "=")
	key = strings.TrimSpace(key)
	if !ok || key == "" {
		return errors.New("must be key=value")
	}
	if *f == nil {
		*f = make(map[string]string)
	}
	(*f)[key] = val
	return nil
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	return runWithClient(ctx, args, getenv, stdout, stderr, newHTTPClient())
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func runWithClient(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, client httpDoer) int {
	opts, ok := parseOptions(args, getenv, stderr)
	if !ok {
		return 2
	}
	eventURL, err := eventEndpoint(opts.baseURL, opts.allowHTTP)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "post-event: --base-url must be an https origin (or http with --allow-http)")
		return 2
	}
	return postEvent(ctx, client, eventURL, opts, stdout, stderr)
}

type commandOptions struct {
	baseURL         string
	credential      string
	externalID      string
	eventType       string
	description     string
	longDescription string
	allowHTTP       bool
	tags            stringMapFlag
}

func parseOptions(args []string, getenv func(string) string, stderr io.Writer) (commandOptions, bool) {
	opts := commandOptions{
		baseURL:   envOrDefault(getenv, "PCR_URL", defaultBaseURL),
		eventType: "deployment",
	}
	flags := flag.NewFlagSet("post-event", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.baseURL, "base-url", opts.baseURL, "PCR origin")
	flags.StringVar(&opts.externalID, "external-id", "", "stable producer event ID (required)")
	flags.StringVar(&opts.eventType, "event-type", opts.eventType, "event category")
	flags.StringVar(&opts.description, "description", "", "short change description (required)")
	flags.StringVar(&opts.longDescription, "long-description", "", "optional detailed description")
	flags.BoolVar(&opts.allowHTTP, "allow-http", false, "allow an http:// base URL for local testing only")
	flags.Var(&opts.tags, "tag", "event tag as key=value; repeatable")
	if err := flags.Parse(args); err != nil {
		return commandOptions{}, false
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "post-event: unexpected positional arguments")
		return commandOptions{}, false
	}
	opts.externalID = strings.TrimSpace(opts.externalID)
	opts.eventType = strings.TrimSpace(opts.eventType)
	opts.description = strings.TrimSpace(opts.description)
	opts.longDescription = strings.TrimSpace(opts.longDescription)
	if opts.externalID == "" || opts.description == "" || opts.eventType == "" {
		_, _ = fmt.Fprintln(stderr, "post-event: --external-id, --event-type, and --description must be non-empty")
		return commandOptions{}, false
	}
	opts.credential = strings.TrimSpace(getenv("PCR_CREDENTIAL"))
	if opts.credential == "" {
		_, _ = fmt.Fprintln(stderr, "post-event: PCR_CREDENTIAL is required")
		return commandOptions{}, false
	}
	return opts, true
}

func eventEndpoint(baseURL string, allowHTTP bool) (string, error) {
	origin, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	validScheme := origin.Scheme == "https" || (allowHTTP && origin.Scheme == "http")
	if origin.Host == "" || origin.User != nil || !validScheme {
		return "", errors.New("invalid PCR origin")
	}
	origin.Path = strings.TrimRight(origin.Path, "/") + "/api/v1/events"
	origin.RawQuery = ""
	origin.Fragment = ""
	return origin.String(), nil
}

func postEvent(ctx context.Context, client httpDoer, eventURL string, opts commandOptions, stdout, stderr io.Writer) int {
	// Identity is deliberately absent: PCR derives it from Beyond's verified
	// headers and must never trust a producer-supplied actor name.
	payload, err := json.Marshal(struct {
		ExternalID      string            `json:"external_id"`
		EventType       string            `json:"event_type"`
		Description     string            `json:"description"`
		LongDescription string            `json:"long_description,omitempty"`
		Tags            map[string]string `json:"tags,omitempty"`
	}{
		ExternalID:      opts.externalID,
		EventType:       opts.eventType,
		Description:     opts.description,
		LongDescription: opts.longDescription,
		Tags:            opts.tags,
	})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "post-event: encode request")
		return 1
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, eventURL, bytes.NewReader(payload))
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "post-event: build request")
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+opts.credential)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "post-event: request failed")
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "post-event: read response")
		return 1
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(stderr, "post-event: PCR returned HTTP %d\n", resp.StatusCode)
		return 1
	}

	var event model.ChangeEvent
	if err := json.Unmarshal(body, &event); err != nil {
		_, _ = fmt.Fprintln(stderr, "post-event: PCR returned an invalid event response")
		return 1
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "post-event: encode response")
		return 1
	}
	_, _ = fmt.Fprintln(stdout, string(encoded))
	return 0
}

func envOrDefault(getenv func(string) string, name, fallback string) string {
	if value := strings.TrimSpace(getenv(name)); value != "" {
		return value
	}
	return fallback
}
