package proxy

import "testing"

func TestUpstreamURLDropsRepeatedV1(t *testing.T) {
	got, err := UpstreamURL("https://cli-chat-proxy.grok.com/v1", "/v1/responses")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://cli-chat-proxy.grok.com/v1/responses" {
		t.Fatalf("got %s", got)
	}
}

func TestUpstreamURLKeepsQuery(t *testing.T) {
	got, err := UpstreamURL("https://api.deepseek.com/", "/v1/responses?beta=true")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://api.deepseek.com/v1/responses?beta=true" {
		t.Fatalf("got %s", got)
	}
}
