module github.com/ranokay/navidrome-better-lyrics-plugin

go 1.25.0

require (
	github.com/extism/go-pdk v1.1.3
	github.com/navidrome/navidrome/plugins/pdk/go v0.0.0-20260711131814-be10f89c1179
)

replace github.com/navidrome/navidrome/plugins/pdk/go => github.com/ranokay/navidrome/plugins/pdk/go v0.0.0-20260823024204-f950db37d9b4

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/stretchr/objx v0.5.2 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
