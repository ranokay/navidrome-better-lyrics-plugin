package main

import (
	"encoding/json"
	"fmt"
)

// parseControlMerge decodes the active publication record. A missing record
// is handled by the caller; a present but malformed record is an error so the
// provider treats the capability as unknown instead of an older host.
func parseControlMerge(raw []byte) (bool, error) {
	var response struct {
		ControlMerge bool `json:"controlMerge"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return false, fmt.Errorf("decode active publication record: %w", err)
	}
	return response.ControlMerge, nil
}
