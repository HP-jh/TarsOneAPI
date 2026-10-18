package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHealthHandler(t *testing.T) {
	client := NewClient("", "")
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	withClient(client, healthHandler)(w, req)
	if w.Code != http.StatusOK { t.Fatalf("expected 200, got %d", w.Code) }
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil { t.Fatal(err) }
	if resp["status"] != "ok" { t.Fatalf("expected ok, got %v", resp["status"]) }
	up, ok := resp["upstream"].(map[string]interface{})
	if !ok { t.Fatal("expected upstream") }
	if _, ok := up["latency_ms"]; !ok { t.Fatal("expected latency_ms") }
}

func TestToolsHandler(t *testing.T) {
	client := NewClient("", "")
	req := httptest.NewRequest(http.MethodGet, "/tools", nil)
	w := httptest.NewRecorder()
	withClient(client, toolsHandler)(w, req)
	if w.Code != http.StatusOK { t.Fatalf("expected 200, got %d", w.Code) }
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil { t.Fatal(err) }
	tools, ok := resp["tools"].([]interface{})
	if !ok || len(tools) == 0 { t.Fatal("expected tools") }
}

func TestInvokeHandler_MethodNotAllowed(t *testing.T) {
	client := NewClient("", "")
	req := httptest.NewRequest(http.MethodGet, "/invoke", nil)
	w := httptest.NewRecorder()
	withClient(client, invokeHandler)(w, req)
	if w.Code != http.StatusMethodNotAllowed { t.Fatalf("expected 405, got %d", w.Code) }
}

func TestInvokeHandler_UnknownTool(t *testing.T) {
	client := NewClient("", "")
	body, _ := json.Marshal(map[string]interface{}{ "tool": "xxx_unknown", "params": map[string]interface{}{} })
	req := httptest.NewRequest(http.MethodPost, "/invoke", bytes.NewReader(body))
	w := httptest.NewRecorder()
	withClient(client, invokeHandler)(w, req)
	if w.Code != http.StatusBadRequest { t.Fatalf("expected 400, got %d", w.Code) }
}

func TestClientReq_MockServer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{ "status": "ok" })
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClient(srv.URL, "test-key")
	res, err := client.req(context.Background(), "GET", "/health", nil)
	if err != nil { t.Fatalf("unexpected err: %v", err) }
	if res["status"] != "ok" { t.Fatalf("expected ok, got %v", res["status"]) }
}

func TestClientReqWithRetry(t *testing.T) {
	attempts := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClient(srv.URL, "")
	res, err := client.reqWithRetry(context.Background(), "GET", "/health", nil)
	if err != nil { t.Fatalf("unexpected err: %v", err) }
	if res["status"] != "ok" { t.Fatalf("expected ok, got %v", res["status"]) }
	if attempts != 2 { t.Fatalf("expected 2 attempts, got %d", attempts) }
}

func TestLoadConfig_File(t *testing.T) {
	os.Setenv("TARS_MCP_ENDPOINT", "")
	os.Setenv("TARS_MCP_API_KEY", "")
	os.Setenv("TARS_MCP_MODE", "")
	os.WriteFile("config.json", []byte(`{"endpoint":"https://file.example.com","api_key":"filekey","mode":"http"}`), 0644)
	defer os.Remove("config.json")
	cfg := loadConfig()
	if cfg.Endpoint != "https://file.example.com" { t.Fatalf("expected file endpoint, got %s", cfg.Endpoint) }
	if cfg.APIKey != "filekey" { t.Fatalf("expected file key, got %s", cfg.APIKey) }
	if cfg.Mode != "http" { t.Fatalf("expected http mode, got %s", cfg.Mode) }
}

func TestLoadConfig_EnvOverridesFile(t *testing.T) {
	os.Setenv("TARS_MCP_ENDPOINT", "https://env.example.com")
	os.Setenv("TARS_MCP_API_KEY", "envkey")
	os.Setenv("TARS_MCP_MODE", "")
	os.WriteFile("config.json", []byte(`{"endpoint":"https://file.example.com","api_key":"filekey","mode":"http"}`), 0644)
	defer os.Remove("config.json")
	cfg := loadConfig()
	if cfg.Endpoint != "https://env.example.com" { t.Fatalf("expected env endpoint, got %s", cfg.Endpoint) }
	if cfg.APIKey != "envkey" { t.Fatalf("expected env key, got %s", cfg.APIKey) }
	if cfg.Mode != "http" { t.Fatalf("expected http mode, got %s", cfg.Mode) }
	os.Unsetenv("TARS_MCP_ENDPOINT")
	os.Unsetenv("TARS_MCP_API_KEY")
}

func TestTokenBucket(t *testing.T) {
	tb := newTokenBucket(1000, 2)
	if !tb.allow() { t.Fatal("expected first allow") }
	if !tb.allow() { t.Fatal("expected second allow") }
	if tb.allow() { t.Fatal("expected third deny (burst=2)") }
	time.Sleep(20 * time.Millisecond)
	if !tb.allow() { t.Fatal("expected allow after refill") }
}

func TestRateLimitMiddleware(t *testing.T) {
	called := 0
	handler := rateLimit(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	})
	for i := 0; i < 15; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		w := httptest.NewRecorder()
		handler(w, req)
	}
	if called == 15 { t.Fatal("expected some rate limited") }
}

func TestMetricsHandler(t *testing.T) {
	reqCount = 5
	errCount = 1
	reqDur = 100 * time.Millisecond
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	metricsHandler(w, req)
	if w.Code != http.StatusOK { t.Fatalf("expected 200, got %d", w.Code) }
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil { t.Fatal(err) }
	if resp["requests_total"].(float64) != 5 { t.Fatalf("expected 5 requests, got %v", resp["requests_total"]) }
}

func TestLogMiddleware(t *testing.T) {
	called := false
	handler := logMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if !called { t.Fatal("expected handler called") }
}

func TestVersionFlag(t *testing.T) {
	old := Version
	Version = "1.5.0"
	if !strings.Contains(Version, ".") { t.Fatal("invalid version") }
	Version = old
}
