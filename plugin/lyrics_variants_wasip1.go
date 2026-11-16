//go:build wasip1

package main

import (
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

// NotImplementedCode matches the PDK's standard "optional export not
// implemented" return code, which the host treats as unsupported.
const NotImplementedCode int32 = -2

//go:wasmexport nd_lyrics_get_lyrics_v2
func _NdLyricsGetLyricsV2() int32 {
	if providerInstance == nil {
		return NotImplementedCode
	}
	var input getLyricsV2RequestWire
	if err := pdk.InputJSON(&input); err != nil {
		pdk.SetError(err)
		return -1
	}
	output, err := handleLyricsV2(input)
	if err != nil {
		pdk.SetError(err)
		return -1
	}
	if err := pdk.OutputJSON(output); err != nil {
		pdk.SetError(err)
		return -1
	}
	return 0
}

//go:wasmexport nd_lyrics_get_variants
func _NdLyricsGetVariants() int32 {
	if providerInstance == nil {
		return NotImplementedCode
	}
	var input getVariantsRequestWire
	if err := pdk.InputJSON(&input); err != nil {
		pdk.SetError(err)
		return -1
	}
	output, err := handleVariants(input)
	if err != nil {
		pdk.SetError(err)
		return -1
	}
	if err := pdk.OutputJSON(output); err != nil {
		pdk.SetError(err)
		return -1
	}
	return 0
}

//go:wasmexport nd_lyrics_get_original
func _NdLyricsGetOriginal() int32 {
	if providerInstance == nil {
		return NotImplementedCode
	}
	var input getOriginalRequestWire
	if err := pdk.InputJSON(&input); err != nil {
		pdk.SetError(err)
		return -1
	}
	output, err := handleOriginal(input)
	if err != nil {
		pdk.SetError(err)
		return -1
	}
	if err := pdk.OutputJSON(output); err != nil {
		pdk.SetError(err)
		return -1
	}
	return 0
}
