// capture-proxy records what Claude Code sends the model, for the attachment
// calibration in internal/event (ferret-z35, ferret-0qo).
//
// It listens on localhost, saves each request BODY under <dir>/requests/ and
// passes the request on to the Anthropic API unchanged. Headers are never
// written: they carry the login token.
//
// One capture:
//
//	go run ./cmd/capture-proxy -dir ~/.ferret/calibration/<bead>-<yyyymmdd> &
//	ANTHROPIC_BASE_URL=http://127.0.0.1:8377 claude --session-id <uuid> ...
//	cp ~/.claude/projects/<project>/<uuid>.jsonl <dir>/transcript.jsonl
//
// File names are NNNN-HHMMSS.mmm_<path>.json, UTC: the generator
// (TestRegenAttachVisibility) reads a request's time of day back out of the name
// and orders it against the transcript's timestamps.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

func main() {
	dir := flag.String("dir", "", "capture directory; request bodies go under <dir>/requests")
	addr := flag.String("addr", "127.0.0.1:8377", "listen address")
	upstream := flag.String("upstream", "https://api.anthropic.com", "API the requests are passed on to")
	flag.Parse()
	if *dir == "" {
		log.Fatal("capture-proxy: -dir is required")
	}
	target, err := url.Parse(*upstream)
	if err != nil {
		log.Fatalf("capture-proxy: -upstream: %v", err)
	}
	reqDir := filepath.Join(*dir, "requests")
	if err := os.MkdirAll(reqDir, 0o700); err != nil {
		log.Fatalf("capture-proxy: %v", err)
	}
	// A file name is built from the request path; the root keeps every write
	// inside reqDir whatever that path says.
	root, err := os.OpenRoot(reqDir)
	if err != nil {
		log.Fatalf("capture-proxy: %v", err)
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
		},
		FlushInterval: -1, // responses stream; pass each chunk on as it arrives
	}
	var seq atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if err := saveBody(root, seq.Add(1), r); err != nil {
				http.Error(w, "capture-proxy: "+err.Error(), http.StatusBadGateway)
				return
			}
		}
		proxy.ServeHTTP(w, r)
	})
	srv := &http.Server{Addr: *addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("capture-proxy: %s → %s, bodies under %s", *addr, target, reqDir)
	log.Fatal(srv.ListenAndServe())
}

// saveBody writes the request body to disk and puts it back for the proxy.
func saveBody(root *os.Root, n int64, r *http.Request) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	name := fmt.Sprintf("%04d-%s_%s.json", n, time.Now().UTC().Format("150405.000"),
		strings.ReplaceAll(strings.Trim(r.URL.Path, "/"), "/", "_"))
	return root.WriteFile(name, body, 0o600)
}
