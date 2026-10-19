package provider

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

type diagnosticError struct {
	message string
	cause   error
}

func (e *diagnosticError) Error() string { return e.message }
func (e *diagnosticError) Unwrap() error { return e.cause }

// Host RPC errors are strings on WASM, so inspecting only *url.Error would
// leave older hosts' credential-bearing diagnostics exposed.
func safeTransportError(err error) error {
	message := "HTTP transport failed"
	var timeout interface{ Timeout() bool }
	switch {
	case errors.Is(err, context.Canceled):
		message = "HTTP request canceled"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		message = "HTTP request timed out"
	case strings.Contains(strings.ToLower(err.Error()), "deadline exceeded"), strings.Contains(strings.ToLower(err.Error()), "timeout"), strings.Contains(strings.ToLower(err.Error()), "timed out"):
		message = "HTTP request timed out"
	}
	return &diagnosticError{message: message, cause: err}
}

var diagnosticURL = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
var diagnosticQuery = regexp.MustCompile(`\?[^\s"'<>]*`)
var diagnosticCredential = regexp.MustCompile(`(?i)((?:usertoken|user_token|api[_-]?key|access[_-]?token|password)\s*[:=]\s*)[^&\s"<>]+`)

// safeDiagnosticError also covers provider error details that echo configured
// credentials. It changes presentation only, preserving errors.Is/As behavior.
func (p *Provider) safeDiagnosticError(err error) error {
	if err == nil {
		return nil
	}
	message := diagnosticURL.ReplaceAllStringFunc(err.Error(), func(raw string) string {
		parsed, parseErr := url.Parse(raw)
		if parseErr != nil {
			return "[invalid URL]"
		}
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		return parsed.String()
	})
	// Error bodies may echo relative request URIs, including encoded parameter
	// names and noncanonical percent escapes. Remove the complete query.
	message = diagnosticQuery.ReplaceAllString(message, "?[redacted]")
	message = diagnosticCredential.ReplaceAllString(message, "${1}[redacted]")
	config := readProviderConfig(p.configLookup)
	token, _, _ := p.cache.Get("musixmatch:token:v1")
	for _, secret := range []string{config.BetterLyricsAPIKey, config.MusixmatchUserToken, string(token)} {
		if secret == "" {
			continue
		}
		for _, form := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			message = strings.ReplaceAll(message, form, "[redacted]")
		}
	}
	if message == err.Error() {
		return err
	}
	return &diagnosticError{message: message, cause: err}
}
