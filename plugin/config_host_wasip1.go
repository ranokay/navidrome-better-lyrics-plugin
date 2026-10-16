//go:build wasip1

package main

import "github.com/navidrome/navidrome/plugins/pdk/go/host"

func defaultConfigLookup() configLookup {
	return host.ConfigGet
}
