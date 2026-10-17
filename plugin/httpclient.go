package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

const maximumAPIErrorRunes = 160

type apiErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// sendRequest performs one host HTTP call, bounding it by both the per-request
// ceiling and the caller-provided lookup deadline.
func (p *lyricsProvider) sendRequest(source, requestURL string, headers map[string]string, deadline time.Time) (*host.HTTPResponse, error) {
	return p.sendRequestHelper(source, "GET", requestURL, nil, headers, deadline)
}

// postForm performs one host POST call whose body is an encoded form; the
// Content-Type is part of the wire contract (both NetEase and PetitLyrics only
// parse the body when it is declared form-urlencoded).
func (p *lyricsProvider) postForm(source, requestURL string, form url.Values, headers map[string]string, deadline time.Time) (*host.HTTPResponse, error) {
	if headers == nil {
		headers = map[string]string{}
	}
	if headers["Content-Type"] == "" {
		headers["Content-Type"] = "application/x-www-form-urlencoded"
	}
	return p.sendRequestHelper(source, "POST", requestURL, []byte(form.Encode()), headers, deadline)
}

func (p *lyricsProvider) sendRequestHelper(source, method, requestURL string, body []byte, headers map[string]string, deadline time.Time) (*host.HTTPResponse, error) {
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
		return nil, fmt.Errorf("request %s: %w", source, err)
	}
	if response == nil {
		return nil, fmt.Errorf("%s returned no response", source)
	}
	return response, nil
}

func apiResponseError(response *host.HTTPResponse) error {
	return providerResponseError("better lyrics API", response)
}

type providerHTTPError struct {
	source            string
	status            int32
	detail            string
	retryAfterSeconds int64
	globalRateLimit   bool
}

func (e *providerHTTPError) Error() string {
	detail := ""
	if e.detail != "" {
		detail = ": " + e.detail
	}
	retry := ""
	if e.retryAfterSeconds > 0 {
		retry = fmt.Sprintf("; retry after %ds", e.retryAfterSeconds)
	}
	return fmt.Sprintf("%s returned HTTP %d%s%s", e.source, e.status, detail, retry)
}

func providerResponseError(source string, response *host.HTTPResponse) error {
	var payload apiErrorResponse
	_ = json.Unmarshal(response.Body, &payload)

	detail := strings.TrimSpace(payload.Error)
	if detail == "" {
		detail = strings.TrimSpace(payload.Message)
	}
	detailRunes := []rune(detail)
	if len(detailRunes) > maximumAPIErrorRunes {
		detail = string(detailRunes[:maximumAPIErrorRunes]) + "…"
	}
	return &providerHTTPError{source: source, status: response.StatusCode, detail: detail}
}

func rateLimitResponseError(source string, response *host.HTTPResponse, retryAfter time.Duration) error {
	err := providerRateLimitResponseError(source, response, retryAfter)
	responseErr, ok := err.(*providerHTTPError)
	if ok {
		responseErr.globalRateLimit = globalRateLimit(response)
	}
	return err
}

// rateLimitFailure records the response's Retry-After window on the source's
// provider-wide cooldown and returns the degraded error a rate-limited lookup
// produces.
func (p *lyricsProvider) rateLimitFailure(sourceID, label string, response *host.HTTPResponse) error {
	retryAfter := retryAfterDuration(response.Headers, p.now())
	p.rememberCooldown(sourceGlobalCooldownKey(sourceID), retryAfter)
	return rateLimitResponseError(label, response, retryAfter)
}

func providerRateLimitResponseError(source string, response *host.HTTPResponse, retryAfter time.Duration) error {
	err := providerResponseError(source, response)
	responseErr, ok := err.(*providerHTTPError)
	if ok {
		responseErr.retryAfterSeconds = durationSecondsCeil(retryAfter)
	}
	return err
}

func isGlobalRateLimitError(err error) bool {
	var responseErr *providerHTTPError
	if errors.As(err, &responseErr) {
		return responseErr.status == 429 && responseErr.globalRateLimit
	}
	var cooldownErr *rateLimitCooldownError
	return errors.As(err, &cooldownErr) && cooldownErr.global
}
