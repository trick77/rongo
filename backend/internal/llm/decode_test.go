package llm

import "testing"

func TestDecodeReply(t *testing.T) {
	var got struct {
		Ask bool `json:"ask"`
	}
	if err := DecodeReply("Sure, here you go:\n```json\n{\"ask\": true}\n```", &got); err != nil || !got.Ask {
		t.Fatalf("fenced object: ask=%v err=%v", got.Ask, err)
	}
	if err := DecodeReply("no object here", &got); err == nil {
		t.Fatal("prose decoded without error")
	}
}
