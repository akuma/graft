// Package proxy is the thin transport: rewrite bytes, inject credentials,
// stream the response. It does not translate protocols.
package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/akuma/graft/internal/config"
	"github.com/akuma/graft/internal/creds"
	"github.com/akuma/graft/internal/transform"
	"github.com/klauspost/compress/zstd"
)

const headerTimeout = 120 * time.Second

// UpstreamURL joins an upstream base with the path Codex requested.
// Codex always calls /v1/responses. When the base already ends in /v1, the
// repeated segment would 404, so it is dropped.
func UpstreamURL(base, requestPath string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	path, query, _ := strings.Cut(requestPath, "?")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	basePath := strings.TrimRight(u.Path, "/")
	if basePath != "" && (path == basePath || strings.HasPrefix(path, basePath+"/")) {
		path = strings.TrimPrefix(path, basePath)
		if path == "" {
			path = "/"
		}
	}
	u.Path = strings.TrimRight(basePath, "/") + path
	u.RawQuery = query
	return u.String(), nil
}

// Serve listens on 127.0.0.1 until the process is killed.
func Serve(p *config.Provider) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { handle(p, w, r) })
	addr := fmt.Sprintf("127.0.0.1:%d", p.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("graft thin proxy for %s listening on %s -> %s", p.Name, addr, p.Upstream)
	return srv.ListenAndServe()
}

func handle(p *config.Provider, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && (r.URL.Path == "/graft/health" || r.URL.Path == "/health") {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "provider": p.Name, "transport": "thin"})
		return
	}
	var body []byte
	if r.Method != http.MethodGet && r.Body != nil {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "graft could not read request body: " + err.Error()})
			return
		}
		body, err = decodeBody(raw, r.Header.Get("Content-Encoding"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "graft could not decode request body: " + err.Error()})
			return
		}
	}
	if r.Method == http.MethodPost && strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/responses") {
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "graft could not parse json: " + err.Error()})
			return
		}
		if p.Name == "grok" || p.Transport == "thin" {
			result := transform.ForGrok(decoded)
			if len(result.Notes) > 0 {
				log.Printf("%s rewrite %s", p.Name, strings.Join(result.Notes, ", "))
			}
			encoded, err := marshal(result.Body)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			body = encoded
		}
	}
	forward(p, w, r, body, 0)
}

func forward(p *config.Provider, w http.ResponseWriter, r *http.Request, body []byte, attempt int) {
	target, err := UpstreamURL(p.Upstream, r.URL.RequestURI())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	var reader io.Reader
	if r.Method != http.MethodGet {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, reader)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	for k, v := range authHeaders(p) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept-Encoding", "identity")
	client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: headerTimeout}}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("%s upstream unreachable: %v", p.Name, err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "graft upstream unreachable: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusUpgradeRequired) && attempt == 0 {
		log.Printf("%s upstream %d, retrying after credential reload", p.Name, resp.StatusCode)
		io.Copy(io.Discard, resp.Body)
		forward(p, w, r, body, 1)
		return
	}
	if resp.StatusCode >= 400 {
		payload, _ := io.ReadAll(resp.Body)
		log.Printf("%s upstream %d: %s", p.Name, resp.StatusCode, truncate(payload))
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		w.Write(payload)
		return
	}
	w.Header().Set("Content-Type", fallback(resp.Header.Get("Content-Type"), "text/event-stream"))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 2048)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				log.Printf("%s client disconnected mid-stream", p.Name)
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func authHeaders(p *config.Provider) map[string]string {
	h := map[string]string{"Content-Type": "application/json", "Accept": "text/event-stream"}
	switch p.Auth {
	case "borrow":
		session, err := creds.GrokSession()
		if err != nil {
			log.Printf("%s credential: %v", p.Name, err)
			return h
		}
		h["Authorization"] = "Bearer " + session.Key
		h["x-xai-token-auth"] = "xai-grok-cli"
		h["x-grok-client-version"] = creds.GrokClientVersion()
	case "api-key":
		key, err := creds.APIKey(p)
		if err != nil {
			log.Printf("%s credential: %v", p.Name, err)
			return h
		}
		h["Authorization"] = "Bearer " + key
	}
	return h
}

func decodeBody(raw []byte, encoding string) ([]byte, error) {
	switch {
	case strings.Contains(encoding, "gzip"):
		r, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(r)
	case strings.Contains(encoding, "zstd"):
		r, err := zstd.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(r)
	default:
		return raw, nil
	}
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	payload, _ := marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(payload)
}

func truncate(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}

func fallback(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func runDir() string {
	if v := os.Getenv("GRAFT_RUN_DIR"); v != "" {
		return v
	}
	return filepath.Join(config.Home(), "run")
}

// Healthy reports whether this provider's proxy answers /graft/health.
func Healthy(p *config.Provider) bool {
	client := &http.Client{Timeout: 400 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/graft/health", p.Port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var body struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false
	}
	return body.Provider == p.Name
}

// Ensure starts the proxy in the background when it is not already healthy.
// started is false when this provider does not use a proxy, or one was already running.
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
	logPath := filepath.Join(runDir(), p.Name+".log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	cmd := exec.Command(bin, "proxy", "run", "--provider", p.Name)
	cmd.Env = append(os.Environ(), "GRAFT_CONFIG="+cfg.Path)
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		f.Close()
		return false, err
	}
	f.Close()
	pidPath := filepath.Join(runDir(), p.Name+".pid")
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", cmd.Process.Pid)), 0o644); err != nil {
		return false, err
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if Healthy(p) {
			log.Printf("graft thin proxy for %s started on port %d (pid %d)", p.Name, p.Port, cmd.Process.Pid)
			return true, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false, fmt.Errorf("graft proxy for %q failed to start; see %s", p.Name, logPath)
}

// Stop signals the recorded pid. Missing pid files are not an error.
func Stop(p *config.Provider) bool {
	path := filepath.Join(runDir(), p.Name+".pid")
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
