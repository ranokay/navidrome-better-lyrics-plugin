package source

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

const maximumAPIErrorRunes = 160

type apiErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// get performs one host GET through the runtime, labelled for error messages.
func get(rt Runtime, source, requestURL string, headers map[string]string, deadline time.Time) (*host.HTTPResponse, error) {
	return rt.Do(source, "GET", requestURL, nil, headers, deadline)
}

// postForm performs one host POST call whose body is an encoded form; the
// Content-Type is part of the wire contract (both NetEase and PetitLyrics only
// parse the body when it is declared form-urlencoded).
func postForm(rt Runtime, source, requestURL string, form url.Values, headers map[string]string, deadline time.Time) (*host.HTTPResponse, error) {
	if headers == nil {
		headers = map[string]string{}
	}
	if headers["Content-Type"] == "" {
		headers["Content-Type"] = "application/x-www-form-urlencoded"
	}
	return rt.Do(source, "POST", requestURL, []byte(form.Encode()), headers, deadline)
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

func providerRateLimitResponseError(source string, response *host.HTTPResponse, retryAfter time.Duration) error {
	err := providerResponseError(source, response)
	responseErr, ok := err.(*providerHTTPError)
	if ok {
		responseErr.retryAfterSeconds = durationSecondsCeil(retryAfter)
	}
	return err
}

// IsGlobalRateLimitError reports whether the error is the provider-wide rate
// limit the walk and sweep policy key off (not a per-query throttle).
func IsGlobalRateLimitError(err error) bool {
	var responseErr *providerHTTPError
	if errors.As(err, &responseErr) {
		return responseErr.status == 429 && responseErr.globalRateLimit
	}
	var cooldownErr *rateLimitCooldownError
	return errors.As(err, &cooldownErr) && cooldownErr.global
}

func globalRateLimit(response *host.HTTPResponse) bool {
	limitType := strings.ToLower(strings.TrimSpace(headerValue(response.Headers, "X-RateLimit-Type")))
	if limitType != "" {
		return limitType == "exceeded"
	}
	body := strings.ToLower(string(response.Body))
	return !strings.Contains(body, "no cached data") && !strings.Contains(body, "requires cached data")
}

func retryAfterDuration(headers map[string]string, now time.Time) time.Duration {
	return retryAfterDurationWithDefault(headers, now, defaultRateLimitCooldown)
}

func retryAfterDurationWithDefault(headers map[string]string, now time.Time, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(headerValue(headers, "Retry-After"))
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
		maxSeconds := int64(maxRateLimitCooldown / time.Second)
		if seconds > maxSeconds {
			seconds = maxSeconds
		}
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(raw); err == nil && retryAt.After(now) {
		return min(retryAt.Sub(now), maxRateLimitCooldown)
	}
	return min(fallback, maxRateLimitCooldown)
}

func headerValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func durationSecondsCeil(duration time.Duration) int64 {
	return int64((duration + time.Second - 1) / time.Second)
}
