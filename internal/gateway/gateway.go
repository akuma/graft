// Package gateway serves Claude Code's Messages API for a thin provider.
//
// DeepSeek is not served here. Its Anthropic endpoint is native, and graft
// points Claude Code at it. A thin provider, today the Grok subscription
// proxy, speaks Responses, so this process translates and then applies
// transform.ForGrok. It listens on the Codex proxy port plus 100, so a Codex
// session on 8787 and a Claude Code session do not share a listener.
package gateway

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/akuma/graft/internal/bridge"
	"github.com/akuma/graft/internal/config"
	"github.com/akuma/graft/internal/proxy"
	"github.com/akuma/graft/internal/transform"
	"github.com/klauspost/compress/zstd"
)

const (
	portOffset    = 100
	headerTimeout = 120 * time.Second
)

// Port is the Messages listener for this provider.
func Port(p *config.Provider) int { return p.Port + portOffset }

// Prepare turns a Messages request into the Responses body ForGrok will send.
func Prepare(p *config.Provider, msg map[string]any) (body map[string]any, stop []string, notes []string, err error) {
	slug := modelSlug(str(msg["model"]))
	model := p.FindModel(slug)
	levels := model.Reasoning
	converted, err := bridge.ToResponses(msg, bridge.Options{
		AcceptsImages: acceptsImages(p, slug),
		Efforts:       levels,
		DefaultEffort: bridge.PreferredEffort(levels),
	})
	if err != nil {
		return nil, nil, nil, err
	}
	rewritten := transform.ForGrok(converted.Body)
	return rewritten.Body, converted.Stop, rewritten.Notes, nil
}

// Handler is the Messages gateway.
func Handler(p *config.Provider) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { handle(p, w, r) })
	return mux
}

// Serve listens on 127.0.0.1 until the process is killed.
func Serve(p *config.Provider) error {
	addr := fmt.Sprintf("127.0.0.1:%d", Port(p))
	srv := &http.Server{Addr: addr, Handler: Handler(p), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("graft claude gateway for %s listening on %s -> %s", p.Name, addr, p.Upstream)
	return srv.ListenAndServe()
}

func handle(p *config.Provider, w http.ResponseWriter, r *http.Request) {
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case r.Method == http.MethodGet && (path == "/graft/health" || path == "/health"):
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "provider": p.Name, "transport": "claude"})
		return
	case r.Method == http.MethodHead && path == "/api/hello":
		w.WriteHeader(http.StatusOK)
		return
	case r.Method == http.MethodPost && path == "/v1/messages/count_tokens":
		body, err := readBody(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		var msg map[string]any
		if len(body) > 0 && json.Unmarshal(body, &msg) != nil {
			writeError(w, http.StatusBadRequest, "graft could not parse json")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"input_tokens": bridge.EstimateTokens(msg)})
		return
	case r.Method == http.MethodPost && path == "/v1/messages":
		handleMessages(p, w, r)
		return
	default:
		writeError(w, http.StatusNotFound, "graft claude gateway has no "+r.Method+" "+r.URL.Path)
	}
}

func handleMessages(p *config.Provider, w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var msg map[string]any
	if err := json.Unmarshal(raw, &msg); err != nil {
		writeError(w, http.StatusBadRequest, "graft could not parse json: "+err.Error())
		return
	}
	prepared, stop, notes, err := Prepare(p, msg)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(notes) > 0 {
		log.Printf("%s claude rewrite %s", p.Name, strings.Join(notes, ", "))
	}
	stream, _ := msg["stream"].(bool)
	prepared["stream"] = stream
	payload, err := marshal(prepared)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp, err := postUpstream(p, r.Context(), payload, 0)
	if err != nil {
		log.Printf("%s claude upstream unreachable: %v", p.Name, err)
		writeError(w, http.StatusBadGateway, "graft upstream unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		upstream, _ := io.ReadAll(resp.Body)
		log.Printf("%s claude upstream %d: %s", p.Name, resp.StatusCode, truncate(upstream))
		writeError(w, resp.StatusCode, upstreamMessage(upstream))
		return
	}
	model := str(prepared["model"])
	if !stream {
		var decoded map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			writeError(w, http.StatusBadGateway, "graft could not read upstream json: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, bridge.FromResponse(decoded))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	dst := newPingWriter(w)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go dst.ping(ctx)
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		var decoded map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			log.Printf("%s claude upstream was not a stream: %v", p.Name, err)
			return
		}
		wrapped, _ := marshal(map[string]any{"type": "response.completed", "response": decoded})
		resp.Body.Close()
		_ = bridge.TranslateStream(dst, strings.NewReader("data: "+string(wrapped)+"\n\n"), model, stop)
		return
	}
	if err := bridge.TranslateStream(dst, resp.Body, model, stop); err != nil {
		log.Printf("%s claude stream: %v", p.Name, err)
	}
}

func postUpstream(p *config.Provider, ctx context.Context, body []byte, attempt int) (*http.Response, error) {
	target, err := proxy.UpstreamURL(p.Upstream, "/responses")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range proxy.AuthHeaders(p) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept-Encoding", "identity")
	client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: headerTimeout}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusUpgradeRequired) && attempt == 0 {
		log.Printf("%s claude upstream %d, retrying after credential reload", p.Name, resp.StatusCode)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return postUpstream(p, ctx, body, 1)
	}
	return resp, nil
}

// Healthy reports whether the Messages gateway answers /graft/health.
func Healthy(p *config.Provider) bool {
	client := &http.Client{Timeout: 400 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/graft/health", Port(p)))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var body struct {
		Provider  string `json:"provider"`
		Transport string `json:"transport"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false
	}
	return body.Provider == p.Name && body.Transport == "claude"
}

// Ensure starts the gateway when it is not already healthy.
// started is false when this provider is not thin, or a gateway was already up.
func Ensure(cfg *config.Config, p *config.Provider) (started bool, err error) {
	if !p.IsThin() || Healthy(p) {
		return false, nil
	}
	if err = os.MkdirAll(runDir(), 0o755); err != nil {
		return false, err
	}
	bin, err := os.Executable()
	if err != nil {
		return false, err
	}
	logPath := filepath.Join(runDir(), "claude-"+p.Name+".log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	cmd := exec.Command(bin, "gateway", "run", "--provider", p.Name)
	cmd.Env = append(os.Environ(), "GRAFT_CONFIG="+cfg.Path)
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		f.Close()
		return false, err
	}
	f.Close()
	pidPath := filepath.Join(runDir(), "claude-"+p.Name+".pid")
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", cmd.Process.Pid)), 0o644); err != nil {
		return false, err
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if Healthy(p) {
			log.Printf("graft claude gateway for %s started on port %d (pid %d)", p.Name, Port(p), cmd.Process.Pid)
			return true, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false, fmt.Errorf("graft claude gateway for %q failed to start; see %s", p.Name, logPath)
}

// Stop signals the recorded gateway pid. It does not stop the Codex proxy.
func Stop(p *config.Provider) bool {
	path := filepath.Join(runDir(), "claude-"+p.Name+".pid")
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var pid int
	fmt.Sscanf(string(data), "%d", &pid)
	if pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	os.Remove(path)
	return true
}

func runDir() string {
	if v := os.Getenv("GRAFT_RUN_DIR"); v != "" {
		return v
	}
	return filepath.Join(config.Home(), "run")
}

type pingWriter struct {
	w    http.ResponseWriter
	f    http.Flusher
	mu   sync.Mutex
	last time.Time
}

func newPingWriter(w http.ResponseWriter) *pingWriter {
	f, _ := w.(http.Flusher)
	return &pingWriter{w: w, f: f, last: time.Now()}
}

func (p *pingWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n, err := p.w.Write(b)
	if p.f != nil {
		p.f.Flush()
	}
	p.last = time.Now()
	return n, err
}

func (p *pingWriter) ping(ctx context.Context) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			p.mu.Lock()
			if time.Since(p.last) >= 10*time.Second {
				_, _ = p.w.Write([]byte("event: ping\ndata: {\"type\":\"ping\"}\n\n"))
				if p.f != nil {
					p.f.Flush()
				}
				p.last = time.Now()
			}
			p.mu.Unlock()
		}
	}
}

func acceptsImages(p *config.Provider, slug string) bool {
	for _, model := range p.Models {
		if model.Slug != slug {
			continue
		}
		for _, kind := range model.Input {
			if kind == "image" {
				return true
			}
		}
		return false
	}
	return false
}

func modelSlug(model string) string {
	if i := strings.Index(model, "["); i > 0 && strings.HasSuffix(model, "]") {
		return model[:i]
	}
	return model
}

func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("graft could not read request body: %w", err)
	}
	return decodeBody(raw, r.Header.Get("Content-Encoding"))
}

func decodeBody(raw []byte, encoding string) ([]byte, error) {
	switch {
	case strings.Contains(encoding, "gzip"):
		reader, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		return io.ReadAll(reader)
	case strings.Contains(encoding, "zstd"):
		reader, err := zstd.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		return io.ReadAll(reader)
	default:
		return raw, nil
	}
}

func upstreamMessage(payload []byte) string {
	var decoded map[string]any
	if json.Unmarshal(payload, &decoded) == nil {
		if errObj, ok := decoded["error"].(map[string]any); ok {
			if msg := str(errObj["message"]); msg != "" {
				return msg
			}
		}
	}
	if len(payload) == 0 {
		return "upstream rejected the request"
	}
	if len(payload) > 2000 {
		payload = payload[:2000]
	}
	return string(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	kind := "invalid_request_error"
	switch {
	case status == http.StatusUnauthorized:
		kind = "authentication_error"
	case status == http.StatusTooManyRequests:
		kind = "rate_limit_error"
	case status >= 500:
		kind = "api_error"
	}
	writeJSON(w, status, map[string]any{
		"type":  "error",
		"error": map[string]any{"type": kind, "message": message},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	payload, _ := marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(payload)
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func truncate(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
