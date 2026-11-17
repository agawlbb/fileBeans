package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type brokerClient struct {
	client  *http.Client
	runtime string
	roots   []Root
	mu      sync.Mutex
	known   []VaultStatus
}

func newBrokerClient(runtime string, roots []Root) *brokerClient {
	return &brokerClient{runtime: runtime, roots: append([]Root(nil), roots...), client: &http.Client{
		Timeout:       45 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(runtime, "broker.sock"))
		}},
	}}
}

func (c *brokerClient) request(method, route string, value any, requestID string, out any) error {
	var data []byte
	var e error
	if value != nil {
		data, e = json.Marshal(value)
		if e != nil {
			return e
		}
	}
	defer clear(data)
	req, e := http.NewRequest(method, "http://broker/v1/"+route, bytes.NewReader(data))
	if e != nil {
		return errVault
	}
	req.Header.Set("Content-Type", "application/json")
	if requestID != "" {
		req.Header.Set("Idempotency-Key", requestID)
	}
	resp, e := c.client.Do(req)
	if e != nil {
		return errVault
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&failure)
		switch failure.Code {
		case "busy":
			return ErrVaultBusy
		case "key":
			return ErrVaultKey
		case "unsafe":
			return ErrUnsafe
		case "conflict":
			return ErrConflict
		case "locked":
			return ErrVaultLocked
		default:
			return errVault
		}
	}
	if out != nil {
		if e = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); e != nil {
			return errVault
		}
	}
	return nil
}

func (c *brokerClient) statuses() ([]VaultStatus, error) {
	var statuses []VaultStatus
	if e := c.request("GET", "vaults", nil, "", &statuses); e != nil {
		return nil, e
	}
	c.mu.Lock()
	c.known = append([]VaultStatus(nil), statuses...)
	c.mu.Unlock()
	return statuses, nil
}

func (c *brokerClient) unavailableStatuses() []VaultStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]VaultStatus{}, c.known...)
	for i := range out {
		out[i].State = "error"
	}
	return out
}

func (c *brokerClient) ready() bool {
	var status struct {
		Version int  `json:"version"`
		Ready   bool `json:"ready"`
	}
	return c.request("GET", "ready", nil, "", &status) == nil && status.Version == brokerProtocol && status.Ready
}

func encodeVaultKey(key []byte) string {
	prefix := "FBK1:" + hex.EncodeToString(key)
	h := sha256.Sum256([]byte(prefix))
	return prefix + ":" + hex.EncodeToString(h[:4])
}

func (c *brokerClient) action(id, action string, key []byte) error {
	if !validBrokerID(id) || action != "lock" && action != "unlock" {
		return ErrUnsafe
	}
	var value any
	if action == "unlock" {
		value = map[string]string{"key": encodeVaultKey(key)}
	}
	return c.request("POST", "vaults/"+id+"/"+action, value, "", nil)
}

func (c *brokerClient) create(source, parent, name string, key []byte, requestID string) (VaultStatus, error) {
	var status VaultStatus
	e := c.request("POST", "vaults", map[string]string{"source": source, "parent": parent, "name": name, "key": encodeVaultKey(key)}, requestID, &status)
	return status, e
}

func (c *brokerClient) close() { c.client.CloseIdleConnections() }

func (m *vaultManager) configs() []VaultConfig {
	if m == nil {
		return nil
	}
	if m.remote != nil {
		lease, e := m.guard.Acquire()
		if e != nil {
			return nil
		}
		defer lease.Close()
		statuses := lease.Snapshot.Vaults
		out := make([]VaultConfig, 0, len(statuses))
		for _, s := range statuses {
			for _, r := range m.remote.roots {
				if r.Name == s.Source {
					out = append(out, VaultConfig{ID: s.ID, Source: s.Source, Path: s.Path,
						Mount: filepath.Join(m.remote.runtime, "mounts", s.ID), Cipher: filepath.Join(r.Path, brokerStore, s.ID)})
				}
			}
		}
		return out
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]VaultConfig, 0, len(m.states))
	for _, v := range m.states {
		out = append(out, v.config)
	}
	return out
}

func (m *vaultManager) pathVault(source, path string) *VaultConfig {
	p, e := clean(path)
	if e != nil {
		return nil
	}
	for _, v := range m.configs() {
		root := v.Path
		if root == "" {
			root = "/"
		}
		if v.Source == source && within("/"+strings.TrimPrefix(p, "./"), root) {
			return &v
		}
	}
	return nil
}

func (s *Service) stagePath(j *Job) string {
	base := ""
	if v := s.vaults.pathVault(j.Request.Destination.Source, j.Request.Destination.Path); v != nil {
		base = strings.TrimPrefix(v.Path, "/")
	}
	return filepath.Join(base, ".filebeans-stage", j.ID)
}

func (m *vaultManager) remoteCipher(sources []string) (cipher.AEAD, error) {
	lease, e := m.guard.Acquire()
	if e != nil {
		return nil, protectionError(e)
	}
	defer lease.Close()
	for _, source := range sources {
		found := false
		for _, v := range lease.Snapshot.Vaults {
			if source == "vault:"+v.ID {
				found = true
				var root string
				for _, r := range m.remote.roots {
					if r.Name == v.Source {
						root = r.Path
					}
				}
				if root == "" {
					return nil, ErrUnsafe
				}
				if e = lease.Check(filepath.Join(root, v.Path), false); e != nil {
					return nil, protectionError(e)
				}
			}
		}
		if !found {
			return nil, ErrVaultLocked
		}
	}
	var response struct {
		Key []byte `json:"key"`
	}
	if e = m.remote.request("GET", "metadata-key", nil, "", &response); e != nil {
		return nil, e
	}
	defer clear(response.Key)
	if len(response.Key) != 32 {
		return nil, ErrVaultLocked
	}
	block, e := aes.NewCipher(response.Key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(block)
}

func brokerFail(w http.ResponseWriter, e error) {
	code, status, message := "setup", 503, "Protected-folder service unavailable"
	switch {
	case errors.Is(e, ErrVaultBusy):
		code, status, message = "busy", 409, "Protected folder is busy"
	case errors.Is(e, ErrVaultKey):
		code, status, message = "key", 409, "The encryption key does not match"
	case errors.Is(e, ErrVaultLocked):
		code, status, message = "locked", 409, "Protected folder is locked"
	case errors.Is(e, ErrConflict):
		code, status, message = "conflict", 409, "Destination exists or source changed"
	case errors.Is(e, ErrUnsafe):
		code, status, message = "unsafe", 400, "Invalid protected-folder location"
	case errors.Is(e, ErrAuthorization):
		code, status, message = "access", 403, "Protected-folder service access denied"
	}
	reply(w, status, map[string]string{"error": message, "code": code})
}
