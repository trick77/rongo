package threads

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// newToken mints 128 bits as 22 URL-safe characters.
//
// Two things are minted this way and they are not the same thing. A share
// token is the whole authorisation for a public page: it can never be a thread
// id or a slug, because the URL is all that stands between the link and the
// conversation. A thread's public_id is only an address — the session cookie
// still authorises it — but it is unguessable for a second reason: a row
// number in the address bar says how many threads exist on the box.
func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
