module github.com/ranokay/navidrome-better-lyrics-plugin

go 1.27.4

require (
	github.com/extism/go-pdk v1.1.3
	github.com/navidrome/navidrome/plugins/pdk/go v0.0.0-20261115062214-690a23438884
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
	go.yaml.in/yaml/v4 v4.0.0
)

require (
	github.com/dlclark/regexp2 v1.12.0 // indirect
	github.com/stretchr/objx v0.8.0 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	go.yaml.in/yaml/v3 v3.0.6 // indirect
	golang.org/x/mod v0.43.0 // indirect
	golang.org/x/text v0.44.0 // indirect
)

replace github.com/navidrome/navidrome/plugins/pdk/go => github.com/ranokay/navidrome/plugins/pdk/go v0.0.0-20261024092602-a730db2a7cd9
