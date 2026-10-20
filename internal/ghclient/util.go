package ghclient

import (
	"encoding/base64"
	"strings"
)

// decodeBase64Content decodes the GitHub /contents API response body.
// GitHub returns base64-encoded content split across newlines.
func decodeBase64Content(s string) ([]byte, error) {
	clean := strings.NewReplacer("\n", "", "\r", "").Replace(s)
	return base64.StdEncoding.DecodeString(clean)
}
