package agents

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// RequesterToken is the LEA_AGENT_TOKEN of agent in container (#160): 32 hex digits of
// HMAC-SHA256(key), apart from the LockHTTP proxy password derived from the same host key. It is
// bound to the agent's incarnation: the same agent resumed gets the same token again, so the
// ticket it asked for still runs; an agent created again under the same name gets another one,
// and cannot run the tickets its predecessor had approved.
func RequesterToken(key []byte, container, agent, incarnation string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("requester\x00" + container + "\x00" + agent + "\x00" + incarnation))
	return hex.EncodeToString(m.Sum(nil))[:32]
}
