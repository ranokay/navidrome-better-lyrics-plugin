//go:build !wasip1

package main

// defaultConfigLookup returns an empty config reader for native test builds; tests
// inject their own lookup when they exercise configuration behavior.
func defaultConfigLookup() configLookup {
	return func(string) (string, bool) { return "", false }
}
