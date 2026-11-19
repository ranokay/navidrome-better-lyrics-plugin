package main

import (
	"testing"
)

func TestParseControlMerge(t *testing.T) {
	t.Parallel()
	capable, err := parseControlMerge([]byte(`{"handled":true,"generation":"g","controlMerge":true}`))
	if err != nil || !capable {
		t.Fatalf("capable parse = %v, %v", capable, err)
	}
	older, err := parseControlMerge([]byte(`{"handled":true,"generation":"g"}`))
	if err != nil || older {
		t.Fatalf("older parse = %v, %v", older, err)
	}
	if _, err := parseControlMerge([]byte(`not-json`)); err == nil {
		t.Fatal("malformed active record was accepted")
	}
}
