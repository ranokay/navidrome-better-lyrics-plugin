package provider

import (
	"fmt"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

// Do performs one host HTTP call through the runtime, bounding it by both the
// per-request ceiling and the caller-provided lookup deadline.
func (p *Provider) Do(sourceName, method, requestURL string, body []byte, headers map[string]string, deadline time.Time) (*host.HTTPResponse, error) {
	timeout, err := p.requestTimeoutMilliseconds(deadline)
	if err != nil {
		return nil, err
	}
	noFollowRedirects := false
	for name, value := range headers {
		if strings.EqualFold(name, "X-API-Key") && value != "" {
			// The host allowlist spans several providers. An authenticated
			// endpoint must not forward its key to another allowed host.
			noFollowRedirects = true
		}
	}
	response, err := p.send(host.HTTPRequest{
		Method:            method,
		URL:               requestURL,
		Body:              body,
		Headers:           headers,
		TimeoutMs:         timeout,
		NoFollowRedirects: noFollowRedirects,
	})
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", sourceName, safeTransportError(err))
	}
	if response == nil {
		return nil, fmt.Errorf("%s returned no response", sourceName)
	}
	if len(response.Body) > source.MaxHTTPResponseBytes {
		return nil, fmt.Errorf("%s response exceeds %d-byte limit", sourceName, source.MaxHTTPResponseBytes)
	}
	return response, nil
}
