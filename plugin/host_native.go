//go:build !wasip1

package main

import "github.com/ranokay/navidrome-better-lyrics-plugin/internal/provider"

// The PDK's native host stubs route through an unset testify mock, so native
// test builds get inert seams: the provider's nil-defaults already fail open,
// and tests inject their own dependencies when they exercise host behavior.

func defaultStore() provider.Store { return nil }

func defaultConfigLookup() provider.ConfigLookup { return nil }

func createSweepQueue(string, int32) error { return nil }

func enqueueTask(string, []byte) error { return nil }

func logDebug(string) {}
