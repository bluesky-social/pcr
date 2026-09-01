// Command pcr-seed loads a JSON fixture through the public create-event API.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sarahmaeve/go-prod-change-registry/internal/fixture"
	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithClient(args, stdout, stderr, &http.Client{Timeout: 10 * time.Second})
}

func runWithClient(args []string, stdout, stderr io.Writer, client *http.Client) int {
	flags := flag.NewFlagSet("pcr-seed", flag.ContinueOnError)
	flags.SetOutput(stderr)
	baseURL := flags.String("base-url", "http://127.0.0.1:8080", "Base URL of the running registry")
	token := flags.String("token", os.Getenv("PCR_TOKEN"), "Bearer token (defaults to PCR_TOKEN)")
	fixturePath := flags.String("fixture", "", "Path to a JSON fixture")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *fixturePath == "" {
		_, _ = fmt.Fprintln(stderr, "error: --fixture is required")
		return 2
	}
	if *token == "" {
		_, _ = fmt.Fprintln(stderr, "error: --token or PCR_TOKEN is required")
		return 2
	}

	file, err := os.Open(*fixturePath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: open fixture: %v\n", err)
		return 1
	}
	defer func() { _ = file.Close() }()

	events, err := fixture.Load(file)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	creator := &apiCreator{
		baseURL: strings.TrimRight(*baseURL, "/"),
		token:   *token,
		client:  client,
	}
	if _, err := fixture.Apply(context.Background(), creator, events); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	_, _ = fmt.Fprintf(stdout, "seeded %d events from %s\n", len(events), *fixturePath)
	return 0
}

type apiCreator struct {
	baseURL string
	token   string
	client  *http.Client
}

func (c *apiCreator) Create(ctx context.Context, request *model.CreateChangeRequest) (*model.ChangeEvent, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/events", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.token)
	httpRequest.Header.Set("Content-Type", "application/json")

	response, err := c.client.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("create event: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("create event: status %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}

	var event model.ChangeEvent
	if err := json.NewDecoder(response.Body).Decode(&event); err != nil {
		return nil, fmt.Errorf("decode created event: %w", err)
	}
	return &event, nil
}
