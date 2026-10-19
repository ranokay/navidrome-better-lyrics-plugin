module github.com/ranokay/navidrome-better-lyrics-plugin

go 1.27.2

require (
	github.com/extism/go-pdk v1.1.3
	github.com/navidrome/navidrome/plugins/pdk/go v0.0.0-20261018080024-df77c462e85d
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/navidrome/navidrome/plugins/pdk/go => github.com/ranokay/navidrome/plugins/pdk/go v0.0.0-20261019045058-409df205d6ce

// Pin test-only transitive modules too, so the full module graph stays current.
// go mod tidy can prune these overrides and select older upstream requirements.
require (
	github.com/creack/pty v1.1.24 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/kr/pty v1.1.8 // indirect
	github.com/pkg/diff v0.0.0-20241224192749-4e6772a4315c // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	github.com/stretchr/objx v0.8.0 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	github.com/yuin/goldmark v1.8.6 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/telemetry v0.0.0-20261016163328-824aa3d1d8dd // indirect
	golang.org/x/tools v0.52.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)
