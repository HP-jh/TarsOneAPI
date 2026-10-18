package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

var Version = "1.5.0"
const RepoName = "TarsOneAPI"

// ========== Config (v1.1.0) ==========
type Config struct {
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"api_key"`
	Mode     string `json:"mode"`
}

func loadConfig() Config {
	cfg := Config{
		Endpoint: os.Getenv("TARS_MCP_ENDPOINT"),
		APIKey:   os.Getenv("TARS_MCP_API_KEY"),
		Mode:     os.Getenv("TARS_MCP_MODE"),
	}
	if data, err := os.ReadFile("config.json"); err == nil {
		var fileCfg Config
		if err := json.Unmarshal(data, &fileCfg); err == nil {
			if cfg.Endpoint == "" { cfg.Endpoint = fileCfg.Endpoint }
			if cfg.APIKey == "" { cfg.APIKey = fileCfg.APIKey }
			if cfg.Mode == "" { cfg.Mode = fileCfg.Mode }
		} else {
			log.Printf("[warn] config.json parse error: %v", err)
		}
	}
	if cfg.Mode == "" { cfg.Mode = "stdio" }
	return cfg
}

// ========== Client (v1.3.0 enhanced) ==========
type Client struct {
	Endpoint string
	APIKey   string
	Client   *http.Client
}

func NewClient(endpoint, apiKey string) *Client {
	if endpoint == "" {
		endpoint = "https://api.example.com"
	}
	tr := &http.Transport{
		MaxIdleConns:        100,
		MaxConnsPerHost:     100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{
		Endpoint: strings.TrimRight(endpoint, "/"),
		APIKey:   apiKey,
		Client:   &http.Client{Timeout: 30 * time.Second, Transport: tr},
	}
}

func (c *Client) req(ctx context.Context, method, path string, body interface{}) (map[string]interface{}, error) {
	var bodyR io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyR = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+path, bodyR)
	if err != nil { return nil, err }
	req.Header.Set("User-Agent", RepoName+"/"+Version)
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Client.Do(req)
	if err != nil { return nil, err }
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return map[string]interface{}{"raw": "non-JSON response"}, nil
	}
	return result, nil
}

func (c *Client) reqWithRetry(ctx context.Context, method, path string, body interface{}) (map[string]interface{}, error) {
	var lastErr error
	for i := 0; i < 3; i++ {
		if i > 0 {
			delay := time.Duration(1<<uint(i-1)) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		res, err := c.req(ctx, method, path, body)
		if err == nil {
			return res, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// ========== Metrics (v1.5.0) ==========
var (
	muMetrics sync.RWMutex
	reqCount  int64
	errCount  int64
	reqDur    time.Duration
)

func recordRequest(d time.Duration, err bool) {
	muMetrics.Lock()
	defer muMetrics.Unlock()
	reqCount++
	reqDur += d
	if err {
		errCount++
	}
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	muMetrics.RLock()
	defer muMetrics.RUnlock()
	avgMs := float64(0)
	if reqCount > 0 {
		avgMs = float64(reqDur.Milliseconds()) / float64(reqCount)
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"requests_total": reqCount,
		"errors_total":   errCount,
		"avg_latency_ms": avgMs,
		"version":        Version,
	})
}

// ========== Rate Limiter (v1.4.0) ==========
type tokenBucket struct {
	tokens float64
	last   time.Time
	rate   float64
	cap    float64
	mu     sync.Mutex
}

func newTokenBucket(ratePerSec, burst int) *tokenBucket {
	return &tokenBucket{tokens: float64(burst), last: time.Now(), rate: float64(ratePerSec), cap: float64(burst)}
}

func (tb *tokenBucket) allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(tb.last).Seconds()
	tb.last = now
	tb.tokens += elapsed * tb.rate
	if tb.tokens > tb.cap {
		tb.tokens = tb.cap
	}
	if tb.tokens >= 1 {
		tb.tokens--
		return true
	}
	return false
}

var limiters = sync.Map{}

func rateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if host == "" {
			host = r.RemoteAddr
		}
		v, _ := limiters.LoadOrStore(host, newTokenBucket(60, 10))
		if !v.(*tokenBucket).allow() {
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "请求太频繁，请稍后再试"})
			return
		}
		next(w, r)
	}
}

// ========== Logging (v1.2.0) ==========
func logMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next(rw, r)
		dur := time.Since(start)
		recordRequest(dur, rw.statusCode >= 400)
		log.Printf("[http] %s %s %d %s", r.Method, r.URL.Path, rw.statusCode, dur)
	}
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// ========== Main ==========
func main() {
	version := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *version {
		fmt.Printf("%s v%s\n", RepoName, Version)
		return
	}
	cfg := loadConfig()
	client := NewClient(cfg.Endpoint, cfg.APIKey)
	log.Printf("[%s] starting v%s endpoint=%s mode=%s", RepoName, Version, cfg.Endpoint, cfg.Mode)

	srv := &http.Server{
		Addr:         ":8080",
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	wrap := func(fn func(http.ResponseWriter, *http.Request, *Client)) http.HandlerFunc {
		return logMiddleware(rateLimit(withClient(client, fn)))
	}

	http.HandleFunc("/health", wrap(healthHandler))
	http.HandleFunc("/tools", wrap(toolsHandler))
	http.HandleFunc("/invoke", wrap(invokeHandler))
	http.HandleFunc("/metrics", metricsHandler)

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Printf("[%s] shutting down", RepoName)
		_ = srv.Close()
	}()

	if cfg.Mode == "stdio" {
		runStdioServer(client)
		return
	}
	log.Printf("[%s] HTTP listening on %s", RepoName, srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}

func withClient(client *Client, fn func(http.ResponseWriter, *http.Request, *Client)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions { w.WriteHeader(http.StatusNoContent); return }
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB body limit (v1.4.0)
		}
		fn(w, r, client)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request, client *Client) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err := client.req(ctx, "GET", "/health", nil)
	latency := time.Since(start).Milliseconds()
	up := map[string]interface{}{"healthy": err == nil, "latency_ms": latency}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok", "version": Version, "name": RepoName, "upstream": up,
		"time": time.Now().UTC().Format(time.RFC3339),
	})
}

func toolsHandler(w http.ResponseWriter, r *http.Request, client *Client) {
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"tools": []map[string]interface{}{
		{
			"name":        "oneapi_models",
			"description": "List models managed by OneAPI",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
				"required":   []interface{}{},
			},
		},
		{
			"name":        "oneapi_chat",
			"description": "Chat completion via OneAPI",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"messages": map[string]interface{}{"type": "array", "description": "Parameter messages"}, "model": map[string]interface{}{"type": "string", "description": "Parameter model"}},
				"required":   []interface{}{"model", "messages"},
			},
		},
		},
	})
}

func invokeHandler(w http.ResponseWriter, r *http.Request, client *Client) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "只接受 POST 请求"})
		return
	}
	var req struct { Tool string `json:"tool"`; Params map[string]interface{} `json:"params"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": fmt.Sprintf("解析失败：%v", err)})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	switch req.Tool {
	case "oneapi_models":
		result, err := client.reqWithRetry(ctx, "GET", "/v1/models", nil)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "上游服务暂时不可用，请稍后重试"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"result": result})
	case "oneapi_chat":
		if req.Params["model"] == nil { w.WriteHeader(http.StatusBadRequest); _ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "缺少参数：model"}); return }
		if req.Params["messages"] == nil { w.WriteHeader(http.StatusBadRequest); _ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "缺少参数：messages"}); return }
		body := map[string]interface{}{"messages": req.Params["messages"], "model": req.Params["model"]}
		result, err := client.reqWithRetry(ctx, "POST", "/v1/chat/completions", body)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "上游服务暂时不可用，请稍后重试"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"result": result})
	default:
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": fmt.Sprintf("不认识工具：%s", req.Tool)})
	}
}

func runStdioServer(client *Client) {
	log.Printf("[%s] stdio mode", RepoName)
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var msg map[string]interface{}
		if err := dec.Decode(&msg); err != nil { return }
		id := msg["id"]
		method, _ := msg["method"].(string)
		var result map[string]interface{}
		switch method {
		case "tools/list":
			result = map[string]interface{}{"tools": []map[string]interface{}{{"name": "oneapi_models", "description": "List models managed by OneAPI"}, {"name": "oneapi_chat", "description": "Chat completion via OneAPI"}}}
		case "tools/call":
			params, _ := msg["params"].(map[string]interface{})
			toolName, _ := params["name"].(string)
			args, _ := params["arguments"].(map[string]interface{})
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			result = handleStdioTool(ctx, client, toolName, args)
			cancel()
		default:
			result = map[string]interface{}{"message": fmt.Sprintf("%s v%s ready", RepoName, Version)}
		}
		_ = enc.Encode(map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": result})
	}
}

func handleStdioTool(ctx context.Context, client *Client, toolName string, args map[string]interface{}) map[string]interface{} {
	switch toolName {
	case "oneapi_models":
		result, err := client.reqWithRetry(ctx, "GET", "/v1/models", nil)
		if err != nil {
			return map[string]interface{}{"content": []map[string]string{{"type": "text", "text": fmt.Sprintf("调用失败：%v", err)}}}
		}
		data, _ := json.Marshal(result)
		return map[string]interface{}{"content": []map[string]string{{"type": "text", "text": string(data)}}}
	case "oneapi_chat":
		if args["model"] == nil { return map[string]interface{}{"content": []map[string]string{{"type": "text", "text": "缺少参数：model"}}} }
		if args["messages"] == nil { return map[string]interface{}{"content": []map[string]string{{"type": "text", "text": "缺少参数：messages"}}} }
		result, err := client.reqWithRetry(ctx, "POST", "/v1/chat/completions", map[string]interface{}{"messages": args["messages"], "model": args["model"]})
		if err != nil {
			return map[string]interface{}{"content": []map[string]string{{"type": "text", "text": fmt.Sprintf("调用失败：%v", err)}}}
		}
		data, _ := json.Marshal(result)
		return map[string]interface{}{"content": []map[string]string{{"type": "text", "text": string(data)}}}
	default:
		return map[string]interface{}{"content": []map[string]string{{"type": "text", "text": fmt.Sprintf("不认识工具：%s", toolName)}}}
	}
}
