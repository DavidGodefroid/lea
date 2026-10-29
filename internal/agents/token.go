package agents

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// RequesterToken is agent's LEA_AGENT_TOKEN in container (#160): 32 hex digits of
// HMAC-SHA256(key), apart from the LockHTTP proxy password derived from the same host key. The
// same agent gets the same token again when it is resumed, so the ticket it asked for still runs.
func RequesterToken(key []byte, container, agent string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("requester\x00" + container + "\x00" + agent))
	return hex.EncodeToString(m.Sum(nil))[:32]
}
