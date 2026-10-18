package provider

import (
	"fmt"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

// Do performs one host HTTP call through the runtime, bounding it by both the
// per-request ceiling and the caller-provided lookup deadline.
func (p *Provider) Do(source, method, requestURL string, body []byte, headers map[string]string, deadline time.Time) (*host.HTTPResponse, error) {
	timeout, err := p.requestTimeoutMilliseconds(deadline)
	if err != nil {
		return nil, err
	}
	response, err := p.send(host.HTTPRequest{
		Method:    method,
		URL:       requestURL,
		Body:      body,
		Headers:   headers,
		TimeoutMs: timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", source, safeTransportError(err))
	}
	if response == nil {
		return nil, fmt.Errorf("%s returned no response", source)
	}
	return response, nil
}
