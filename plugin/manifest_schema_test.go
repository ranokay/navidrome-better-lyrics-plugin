package main

import (
	"bytes"
	_ "embed"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed testdata/navidrome-manifest-schema.json
var hostManifestSchema []byte

func TestManifestAndConfigDefaultsMatchPublishedSchemas(t *testing.T) {
	t.Parallel()
	const schemaURL = "https://raw.githubusercontent.com/navidrome/navidrome/refs/tags/v0.64.2/plugins/manifest-schema.json"
	compiler := jsonschema.NewCompiler()
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(hostManifestSchema))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource(schemaURL, document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := jsonschema.UnmarshalJSON(bytes.NewReader(manifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(manifest); err != nil {
		t.Fatalf("manifest violates host schema: %v", err)
	}
	root := manifest.(map[string]any)
	config := root["config"].(map[string]any)["schema"].(map[string]any)
	// Navidrome's manifest contract specifies Draft 7 for plugin configuration.
	config["$schema"] = "http://json-schema.org/draft-07/schema#"
	const configURL = "urn:nd-better-lyrics:config"
	if err := compiler.AddResource(configURL, config); err != nil {
		t.Fatal(err)
	}
	configSchema, err := compiler.Compile(configURL)
	if err != nil {
		t.Fatalf("invalid plugin configuration schema: %v", err)
	}
	defaults := make(map[string]any)
	for name, raw := range config["properties"].(map[string]any) {
		if value, ok := raw.(map[string]any)["default"]; ok {
			defaults[name] = value
		}
	}
	if err := configSchema.Validate(defaults); err != nil {
		t.Fatalf("default configuration violates its schema: %v", err)
	}
}
