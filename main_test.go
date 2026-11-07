package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestVercelJevRequestPinsTypeSafe(t *testing.T) {
	request := newJevRequest("18+", typesafeModel, true)
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != vercelModel {
		t.Fatalf("model = %v, want %q", got["model"], vercelModel)
	}
	options := got["providerOptions"].(map[string]any)
	gateway := options["gateway"].(map[string]any)
	providers := gateway["only"].([]any)
	if len(providers) != 1 || providers[0] != "typesafe-ai" {
		t.Fatalf("provider allowlist = %v, want [typesafe-ai]", providers)
	}
	questions := got["questions"].(map[string]any)
	question := questions["sexual_suggestiveness"].(map[string]any)
	if question["type"] != "boolean" {
		t.Fatalf("question type = %v, want boolean", question["type"])
	}
}

func TestDirectTypeSafeRequestKeepsSystemOneShape(t *testing.T) {
	request := newJevRequest("18+", typesafeModel, false)
	if request.Model != typesafeModel || request.ProviderOptions != nil {
		t.Fatalf("direct request = %#v", request)
	}
	question := request.Questions["sexual_suggestiveness"].(map[string]any)
	if question["type"] != "noul" {
		t.Fatalf("question type = %v, want noul", question["type"])
	}
}

func TestNetlifyEndpoint(t *testing.T) {
	for _, endpoint := range []string{"https://example.netlify.app/api/jev", " https://example.netlify.app/api/jev "} {
		got, err := netlifyEndpoint(endpoint)
		if err != nil || got != "https://example.netlify.app/api/jev" {
			t.Fatalf("netlifyEndpoint(%q) = %q, %v", endpoint, got, err)
		}
	}
	for _, base := range []string{"", "http://gateway.example/typesafe", "https:///typesafe", "https://user:password@gateway.example/typesafe", "https://gateway.example/typesafe?key=secret", "https://gateway.example/typesafe?", "https://gateway.example/typesafe#fragment"} {
		if _, err := netlifyEndpoint(base); err == nil {
			t.Errorf("accepted invalid base URL %q", base)
		}
	}
}

func TestNetlifyScoreUsesFunctionAndSeparateCache(t *testing.T) {
	const content = "18+🍆🍑!!"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/api/jev" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected request: %s %s, authorization=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var request jevRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		question, ok := request.Questions["sexual_suggestiveness"].(map[string]any)
		if request.State != content || request.Model != typesafeModel || request.ProviderOptions != nil || !ok || question["type"] != "noul" {
			t.Errorf("unexpected System One request: %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"sexual_suggestiveness":{"noul":0.42}}}`))
	}))
	defer server.Close()
	cache, err := openScoreCache(filepath.Join(t.TempDir(), "scores.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cache.close()
	client := jevClient{provider: netlifyProvider, providerName: "Netlify Function", model: typesafeModel, endpoint: server.URL + "/api/jev", apiKey: "test-key", httpClient: server.Client(), cache: cache}
	score, cached, err := client.score(context.Background(), content)
	if err != nil || cached || score != 0.42 {
		t.Fatalf("first score = %v, cached=%v, err=%v", score, cached, err)
	}
	score, cached, err = client.score(context.Background(), "18+🍆🍑!")
	if err != nil || !cached || score != 0.42 || calls.Load() != 1 {
		t.Fatalf("cached score = %v, cached=%v, calls=%d, err=%v", score, cached, calls.Load(), err)
	}
	client.provider = typesafeProvider
	_, cached, err = client.score(context.Background(), content)
	if err != nil || cached || calls.Load() != 2 {
		t.Fatalf("TypeSafe reused Netlify cache: cached=%v, calls=%d, err=%v", cached, calls.Load(), err)
	}
}
