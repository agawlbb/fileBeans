// Package vaultbridge connects the pinned Quantum server to the mount broker.
package vaultbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"filebeans/companion/protection"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

var once sync.Once
var guard *protection.Guard
var initError error
var runtimeDir string

func Enabled() bool { return os.Getenv("FILEBEANS_VAULT_RUNTIME") != "" }

func ValidateCreationModes(fileMode, dirMode os.FileMode) error {
	if Enabled() && (fileMode.Perm()&0060 != 0060 || dirMode.Perm()&0070 != 0070) {
		return errors.New("vault consumers require shared-group read/write creation permissions")
	}
	return nil
}

func get() (*protection.Guard, error) {
	if !Enabled() {
		return nil, nil
	}
	once.Do(func() {
		runtimeDir = os.Getenv("FILEBEANS_VAULT_RUNTIME")
		var roots map[string]string
		if json.Unmarshal([]byte(os.Getenv("FILEBEANS_VAULT_SOURCES")), &roots) != nil {
			initError = protection.ErrUnavailable
			return
		}
		guard, initError = protection.New(runtimeDir, roots)
	})
	return guard, initError
}

func Acquire() (*protection.Lease, error) {
	g, e := get()
	if e != nil || g == nil {
		return nil, e
	}
	return g.Acquire()
}

func Read(path string, recursive bool) (*protection.Lease, error) {
	if Hidden(path) {
		return nil, protection.ErrHidden
	}
	l, e := Acquire()
	if e != nil || l == nil {
		return nil, e
	}
	if e = l.Check(path, recursive); e != nil {
		l.Close()
		return nil, e
	}
	return l, nil
}

func Mutation(from, to string) (*protection.Lease, error) {
	if Hidden(from) || to != "" && Hidden(to) {
		return nil, protection.ErrHidden
	}
	l, e := Acquire()
	if e != nil || l == nil {
		return nil, e
	}
	if e = l.Mutation(from, to); e != nil {
		l.Close()
		return nil, e
	}
	return l, nil
}

func Indexable(path string) bool {
	g, e := get()
	return e == nil && (g == nil || g.Indexable(path))
}

func Protected(path string) bool {
	l, e := Read(path, false)
	if e != nil || l == nil {
		return false
	}
	defer l.Close()
	v, _, e := l.Boundary(path)
	return e == nil && v != nil
}

func Hidden(path string) bool {
	if !Enabled() {
		return false
	}
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if part == ".filebeans-vaults" || part == ".filebeans-stage" {
			return true
		}
	}
	return false
}

func Viewable(path string, isDir bool) bool {
	if Hidden(path) {
		return false
	}
	l, e := Acquire()
	if e != nil {
		return false
	}
	if l == nil {
		return true
	}
	defer l.Close()
	v, _, e := l.Boundary(path)
	if e != nil {
		return false
	}
	if v != nil && isDir && filepath.Clean(path) == filepath.Join(sourceRoot(l, v.Source), v.Path) {
		return true
	}
	return l.Check(path, false) == nil
}

func sourceRoot(_ *protection.Lease, source string) string {
	var roots map[string]string
	_ = json.Unmarshal([]byte(os.Getenv("FILEBEANS_VAULT_SOURCES")), &roots)
	return roots[source]
}

// MetadataAllowed excludes protected child names from ordinary database rows.
// The visible protected folder name itself remains public in its parent listing.
func MetadataAllowed(path string) bool {
	l, e := Acquire()
	if e != nil {
		return false
	}
	if l == nil {
		return true
	}
	defer l.Close()
	v, _, e := l.Boundary(path)
	return e == nil && (v == nil || filepath.Clean(path) == filepath.Join(sourceRoot(l, v.Source), v.Path))
}

func CacheDirectory(path string) (string, error) {
	l, e := Read(path, false)
	if e != nil || l == nil {
		return "", e
	}
	defer l.Close()
	private, e := l.CacheDirectory(path)
	if e != nil || private == "" {
		return "", e
	}
	return filepath.Join(private, "cache"), nil
}

func HTTP(next http.Handler) http.Handler {
	if !Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l, e := Acquire()
		if e != nil {
			http.Error(w, "Protected-folder service unavailable", 503)
			return
		}
		defer l.Close()
		// OnlyOffice is an external plaintext consumer and is not part of this
		// broker's lifecycle. Keep its routes closed in protected deployments.
		if strings.Contains(strings.ToLower(r.URL.Path), "onlyoffice") {
			http.Error(w, "Unavailable", 403)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(noStoreWriter{w}, r)
	})
}

type noStoreWriter struct{ http.ResponseWriter }

func (w noStoreWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w noStoreWriter) WriteHeader(code int) {
	w.Header().Set("Cache-Control", "no-store")
	w.ResponseWriter.WriteHeader(code)
}
func (w noStoreWriter) Write(data []byte) (int, error) {
	w.Header().Set("Cache-Control", "no-store")
	return w.ResponseWriter.Write(data)
}
func (w noStoreWriter) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }
func (w noStoreWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

var _ io.Writer = noStoreWriter{}

// Start registers only the fully patched consumer. A caller must already have
// installed the HTTP, index, preview, mutation and metadata hooks.
func Start(ctx context.Context) error {
	if !Enabled() {
		return nil
	}
	if os.Geteuid() == 0 {
		return errors.New("Quantum must run as an unprivileged vault reader")
	}
	l, e := Acquire()
	if e != nil {
		return e
	}
	l.Close()
	if e = guard.RequirePrimaryGroup(); e != nil {
		return e
	}
	// Set once at startup, before workers. Configured group bits must survive
	// creation so the separately identified companion can operate on uploads.
	syscall.Umask(0007)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(runtimeDir, "broker.sock"))
	}}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	body, _ := json.Marshal(map[string]any{"version": protection.Protocol, "features": protection.ConsumerFeatures})
	ping := func() {
		req, e := http.NewRequestWithContext(ctx, "POST", "http://broker/v1/consumer/ready", bytes.NewReader(body))
		if e != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, e := client.Do(req)
		if e == nil {
			resp.Body.Close()
		}
	}
	ping()
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		defer transport.CloseIdleConnections()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ping()
			}
		}
	}()
	return nil
}

// Conflict identifies protected-boundary failures without exposing paths.
func Conflict(err error) bool {
	return errors.Is(err, protection.ErrBoundary) || errors.Is(err, protection.ErrLocked) || errors.Is(err, protection.ErrBusy) || errors.Is(err, protection.ErrHidden)
}
