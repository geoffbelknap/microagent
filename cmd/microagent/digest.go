package main

import "strings"

// shortDigestLen is the number of hex characters kept after the algorithm
// prefix, matching the short image ID docker shows in `docker images`.
const shortDigestLen = 12

// shortDigest returns the first 12 hex characters after the algorithm
// prefix of a content digest (e.g. "sha256:abcdef0123456789..." becomes
// "abcdef012345"). It is used only in human list views (workspace list has
// no digest column; image list and model list do); --json output and
// `image inspect` (writeImageRecord) always carry the full digest string
// unchanged. A digest shorter than 12 hex characters, or without a colon
// prefix at all, is returned as-is (minus any prefix) rather than padded.
func shortDigest(digest string) string {
	hex := digest
	if idx := strings.IndexByte(digest, ':'); idx >= 0 {
		hex = digest[idx+1:]
	}
	if len(hex) > shortDigestLen {
		hex = hex[:shortDigestLen]
	}
	return hex
}
