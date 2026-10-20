package pcrclient

import (
	"net/http"
	"net/url"
)

// The client tests live in package pcrclient_test so they exercise the API the
// way callers do. These are the only internals they need: the response size
// cap probed with an oversized body, the configured redirect policy, and the
// URL builder the path-escaping fuzz target drives directly. Keeping the
// bridge here avoids widening the real API.

const MaxResponseSize = maxResponseSize

func (c *Client) HTTPClient() *http.Client { return c.httpClient }

func (c *Client) Endpoint(segments []string, query url.Values) *url.URL {
	return c.endpoint(segments, query)
}
