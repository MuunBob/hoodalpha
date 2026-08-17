package initdata

import "encoding/base64"

// decodeBase64URL accepts base64url with or without padding. Telegram sends
// the signature unpadded, but tolerating both avoids a spurious rejection if
// that ever changes.
func decodeBase64URL(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}
