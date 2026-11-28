package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"filebeans/companion/protection"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type brokerPeer struct{}

func (b *mountBroker) keyMatches(key []byte) error {
	h := sha256.Sum256(key)
	encoded := []byte(hex.EncodeToString(h[:]))
	paths := []string{b.manager.keyFile}
	for _, v := range b.manager.states {
		paths = append(paths, b.manager.initializationFile(v.config.ID))
	}
	for _, path := range paths {
		known, e := os.ReadFile(path)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return errVault
		}
		if !hmac.Equal(known, encoded) {
			return ErrVaultKey
		}
	}
	return nil
}

func (b *mountBroker) createProtectedFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source string `json:"source"`
		Parent string `json:"parent"`
		Name   string `json:"name"`
		Key    string `json:"key"`
	}
	if e := body(r, &req); e != nil {
		brokerFail(w, e)
		return
	}
	key, e := parseVaultKey(req.Key)
	req.Key = ""
	if e != nil {
		brokerFail(w, e)
		return
	}
	defer clear(key)
	// Readers can need broker status requests to finish. Wait for their
	// leases before taking b.mu, just as global lock/unlock does.
	gate, e := b.exclusiveContext(r.Context())
	if e != nil {
		brokerFail(w, e)
		return
	}
	defer gate.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	if e = b.keyMatches(key); e != nil {
		brokerFail(w, e)
		return
	}
	if time.Since(b.consumerReady) >= 15*time.Second {
		brokerFail(w, errVault)
		return
	}
	if e = b.activateLocations(key); e != nil {
		brokerFail(w, e)
		return
	}
	if e = b.unlockAll(key); e != nil {
		brokerFail(w, e)
		return
	}
	entry, e := b.reserve(req.Source, req.Parent, req.Name, keyHeader(r))
	if e != nil {
		brokerFail(w, e)
		return
	}
	if e = b.open(entry, key); e != nil {
		_ = b.publish()
		brokerFail(w, e)
		return
	}
	for _, status := range b.statuses() {
		if status.ID == entry.ID {
			reply(w, 201, status)
			return
		}
	}
	brokerFail(w, errVault)
}

func (b *mountBroker) handler() http.Handler {
	fail := brokerFail
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/dataset-activity", func(w http.ResponseWriter, r *http.Request) {
		uid, _ := r.Context().Value(brokerPeer{}).(uint32)
		if uid != b.config.ControlUID {
			fail(w, ErrAuthorization)
			return
		}
		var req Item
		if body(r, &req) != nil {
			fail(w, ErrUnsafe)
			return
		}
		p, e := filesystemPath(req.Path)
		if e != nil {
			fail(w, e)
			return
		}
		for _, v := range b.registry.Entries {
			if v.Source == req.Source && v.Trashed == 0 && v.Path != "" && within("/"+strings.TrimPrefix(p, "./"), v.Path) {
				state := b.manager.find(v.ID)
				if state == nil || !state.unlocked {
					fail(w, ErrVaultLocked)
					return
				}
				b.datasetActivity = time.Now()
				reply(w, 200, map[string]bool{"active": true})
				return
			}
		}
		fail(w, ErrUnsafe)
	})
	mux.HandleFunc("GET /v1/metadata-key", func(w http.ResponseWriter, r *http.Request) {
		uid, _ := r.Context().Value(brokerPeer{}).(uint32)
		if uid != b.config.ControlUID {
			fail(w, ErrAuthorization)
			return
		}
		b.manager.mu.Lock()
		defer b.manager.mu.Unlock()
		if len(b.manager.metadataKey) != 32 {
			fail(w, ErrVaultLocked)
			return
		}
		// A derived metadata key, never the filesystem master key. Only the
		// companion's OS identity may receive it over this local socket.
		reply(w, 200, map[string][]byte{"key": b.manager.metadataKey})
	})
	mux.HandleFunc("GET /v1/protection", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, b.protectionStatus()) })
	mux.HandleFunc("GET /v1/vaults", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, b.statuses()) })
	mux.HandleFunc("GET /v1/ready", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"version": brokerProtocol, "ready": time.Since(b.consumerReady) < 15*time.Second})
	})
	mux.HandleFunc("POST /v1/consumer/ready", func(w http.ResponseWriter, r *http.Request) {
		uid, _ := r.Context().Value(brokerPeer{}).(uint32)
		if (!b.config.FilesystemConsumer && uid != b.config.ReaderUID) || (b.config.FilesystemConsumer && uid != b.config.ControlUID) {
			fail(w, ErrAuthorization)
			return
		}
		var req struct {
			Version  int      `json:"version"`
			Features []string `json:"features"`
		}
		if e := body(r, &req); e != nil || req.Version != brokerProtocol {
			fail(w, ErrUnsafe)
			return
		}
		for _, feature := range protection.ConsumerFeatures {
			if !slices.Contains(req.Features, feature) {
				fail(w, ErrUnsafe)
				return
			}
		}
		b.consumerReady = time.Now()
		reply(w, 200, map[string]int{"version": brokerProtocol})
	})
	mux.HandleFunc("POST /v1/vaults", b.createProtectedFolder)
	b.folderRoutes(mux)
	for _, action := range []string{"unlock", "lock"} {
		mux.HandleFunc("POST /v1/vaults/{id}/"+action, func(w http.ResponseWriter, r *http.Request) {
			var entry *brokerEntry
			for i := range b.registry.Entries {
				if b.registry.Entries[i].ID == r.PathValue("id") {
					entry = &b.registry.Entries[i]
					break
				}
			}
			if entry == nil {
				fail(w, os.ErrNotExist)
				return
			}
			var key []byte
			if action == "unlock" {
				var req struct {
					Key string `json:"key"`
				}
				if e := body(r, &req); e != nil {
					fail(w, e)
					return
				}
				var e error
				key, e = parseVaultKey(req.Key)
				req.Key = ""
				if e != nil {
					fail(w, e)
					return
				}
				defer clear(key)
				if e = b.keyMatches(key); e != nil {
					fail(w, e)
					return
				}
				if time.Since(b.consumerReady) >= 15*time.Second {
					fail(w, errVault)
					return
				}
			}
			gate, e := b.exclusive()
			if e != nil {
				fail(w, e)
				return
			}
			defer gate.Close()
			if action == "lock" {
				e = b.lock(*entry)
			} else {
				e = b.open(*entry, key)
			}
			if e != nil {
				_ = b.publish()
				fail(w, e)
				return
			}
			reply(w, 200, b.statuses())
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		uid, ok := r.Context().Value(brokerPeer{}).(uint32)
		if !ok || uid != b.config.ControlUID && uid != b.config.ReaderUID {
			fail(w, ErrAuthorization)
			return
		}
		if uid == b.config.ReaderUID && r.Method != "GET" && r.URL.Path != "/v1/consumer/ready" {
			fail(w, ErrAuthorization)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		isGlobal := strings.HasPrefix(r.URL.Path, "/v1/protection/")
		isLegacy := strings.HasPrefix(r.URL.Path, "/v1/vaults/") && protectionAction(r.URL.Path) != ""
		if r.URL.Path == "/v1/protection-session" {
			if uid != b.config.ControlUID {
				fail(w, ErrAuthorization)
				return
			}
			b.protectionSyncHTTP(w, r)
			return
		}
		if r.Method == "POST" && (isGlobal || isLegacy) {
			b.globalProtection(w, r)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/v1/vaults" {
			// Creation owns its gate/mutex ordering, including cancellable wait.
			mux.ServeHTTP(w, r)
			return
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		mux.ServeHTTP(w, r)
	})
}

// Separate name avoids shadowing key bytes inside the create handler.
func keyHeader(r *http.Request) string { return key(r) }

// RunVaultBroker never opens a SQL connection or loads web-service credentials.
func RunVaultBroker(ctx context.Context, configPath string) error {
	if os.Geteuid() != 0 {
		return ErrAuthorization
	}
	if e := requireBrokerCapabilities(); e != nil {
		return e
	}
	if e := brokerTrustedExecutable(configPath, false); e != nil {
		return e
	}
	data, e := os.ReadFile(configPath)
	if e != nil {
		return e
	}
	var c BrokerConfig
	if e = json.Unmarshal(data, &c); e != nil {
		return ErrUnsafe
	}
	if c.ControlUID == 0 || c.ReaderUID == 0 || c.GID == 0 {
		return ErrUnsafe
	}
	for _, backup := range c.LocationBackups {
		if e = brokerTrustedExecutable(backup, false); e != nil {
			return e
		}
	}
	for _, executable := range []string{c.Gocryptfs, c.Fusermount} {
		if e = brokerTrustedExecutable(executable, true); e != nil {
			return e
		}
	}
	if e = vaultMemoryPolicy(); e != nil {
		return e
	}
	b, e := newMountBroker(c)
	if e != nil {
		return e
	}
	defer b.close()
	idleCtx, stopIdle := context.WithCancel(ctx)
	idleDone := make(chan struct{})
	go func() { defer close(idleDone); b.autoLockVaults(idleCtx) }()
	defer func() { stopIdle(); <-idleDone }()
	path := filepath.Join(c.Runtime, "broker.sock")
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return ErrUnsafe
		}
		if e = os.Remove(path); e != nil {
			return e
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, e := net.Listen("unix", path)
	if e != nil {
		return e
	}
	defer listener.Close()
	if e = os.Chown(path, 0, c.GID); e != nil {
		return e
	}
	if e = os.Chmod(path, 0660); e != nil {
		return e
	}
	server := &http.Server{Handler: b.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 4096,
		ConnContext: brokerPeerContext}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		case <-done:
		}
	}()
	e = server.Serve(listener)
	close(done)
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}

func brokerTrustedExecutable(path string, executable bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrUnsafe
	}
	var st unix.Stat_t
	if unix.Lstat(path, &st) != nil || st.Uid != 0 || st.Mode&0022 != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return ErrUnsafe
	}
	if executable && st.Mode&0111 == 0 {
		return ErrUnsafe
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		if unix.Lstat(parent, &st) != nil || st.Uid != 0 || st.Mode&0022 != 0 || st.Mode&unix.S_IFMT != unix.S_IFDIR {
			return ErrUnsafe
		}
		if parent == "/" {
			break
		}
	}
	return nil
}

func brokerPeerContext(ctx context.Context, conn net.Conn) context.Context {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return ctx
	}
	raw, e := uc.SyscallConn()
	if e != nil {
		return ctx
	}
	var peer *unix.Ucred
	_ = raw.Control(func(fd uintptr) { peer, _ = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if peer == nil {
		return ctx
	}
	return context.WithValue(ctx, brokerPeer{}, peer.Uid)
}
