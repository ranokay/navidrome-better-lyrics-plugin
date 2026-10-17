package source

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

func TestAPIResponseErrorTruncatesDetail(t *testing.T) {
	t.Parallel()

	detail := strings.Repeat("x", maximumAPIErrorRunes+20)
	err := apiResponseError(&host.HTTPResponse{
		StatusCode: 500,
		Body:       []byte(`{"error":"` + detail + `"}`),
	})
	if !strings.HasSuffix(err.Error(), "…") {
		t.Fatalf("apiResponseError() = %q, want truncated detail", err)
	}
}

func TestRetryAfterDuration(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 23, 6, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{name: "seconds", header: "12", want: 12 * time.Second},
		{name: "HTTP date", header: now.Add(45 * time.Second).Format(http.TimeFormat), want: 45 * time.Second},
		{name: "missing", want: defaultRateLimitCooldown},
		{name: "invalid", header: "later", want: defaultRateLimitCooldown},
		{name: "zero", header: "0", want: defaultRateLimitCooldown},
		{name: "negative", header: "-5", want: defaultRateLimitCooldown},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := retryAfterDuration(map[string]string{"retry-after": test.header}, now); got != test.want {
				t.Fatalf("retryAfterDuration() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestRetryAfterDurationCapsExcessiveValues(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 23, 7, 0, 0, 0, time.UTC)
	if got := retryAfterDuration(map[string]string{"Retry-After": "999999999999999999"}, now); got != maxRateLimitCooldown {
		t.Fatalf("numeric Retry-After = %s, want cap %s", got, maxRateLimitCooldown)
	}
	if got := retryAfterDuration(map[string]string{"Retry-After": now.Add(24 * time.Hour).Format(http.TimeFormat)}, now); got != maxRateLimitCooldown {
		t.Fatalf("date Retry-After = %s, want cap %s", got, maxRateLimitCooldown)
	}
}

func TestFormPostsDeclareTheirContentType(t *testing.T) {
	t.Parallel()

	var seen []string
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		seen = append(seen, request.Headers["Content-Type"])
		return &host.HTTPResponse{StatusCode: 404}, nil
	})

	deadline := time.Now().Add(testLookupBudget)
	if _, err := postForm(rt, "Test", "https://example.test/post", url.Values{"s": {"value"}}, map[string]string{}, deadline); err != nil {
		t.Fatalf("postForm() error = %v, want nil", err)
	}

	if len(seen) != 1 || seen[0] != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type headers = %v, want exactly one form-urlencoded POST", seen)
	}
}
