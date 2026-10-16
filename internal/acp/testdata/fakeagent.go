//go:build ignore

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	mode := os.Getenv("FAKE_MODE")
	var promptID any
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var m map[string]any
		json.Unmarshal(sc.Bytes(), &m)
		id := m["id"]
		if m["method"] == nil && id == "perm-1" {
			fmt.Fprintf(os.Stderr, "fake: permission reply %s\n", sc.Bytes())
			fmt.Printf(`{"jsonrpc":"2.0","id":%v,"result":{"stopReason":"end_turn"}}`+"\n", promptID)
			continue
		}
		switch m["method"] {
		case "initialize":
			fmt.Printf(`{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":1,"agentCapabilities":{}}}`+"\n", id)
		case "session/new":
			fmt.Printf(`{"jsonrpc":"2.0","id":%v,"result":{"sessionId":"s1"}}`+"\n", id)
		case "session/prompt":
			if mode == "noreason" {
				fmt.Printf(`{"jsonrpc":"2.0","id":%v,"result":{}}`+"\n", id)
				continue
			}
			if mode == "requests" {
				promptID = id
				fmt.Println(`{"jsonrpc":"2.0","id":"perm-1","method":"session/request_permission","params":{"sessionId":"s1"}}`)
				fmt.Println(`{"jsonrpc":"2.0","id":77,"method":"fs/read_text_file","params":{}}`)
				fmt.Println(`this is not json`)
				continue
			}
			fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"pong"}}}}`)
			fmt.Printf(`{"jsonrpc":"2.0","id":%v,"result":{"stopReason":"end_turn"}}`+"\n", id)
		}
	}
}
