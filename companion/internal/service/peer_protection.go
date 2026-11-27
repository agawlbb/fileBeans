package service

import (
	"bytes"
	"context"
	"crypto/cipher"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
)

// Remote protected names are encrypted by the destination broker's volatile
// metadata key. Reading them additionally requires the source vault unlocked.
func (s *Service) nodeRecordCipher(sources []string, requireRemote bool) (cipher.AEAD, error) {
	local := []string{}
	for _, label := range sources {
		if !strings.HasPrefix(label, "peer-vault:") {
			local = append(local, label)
			continue
		}
		if !requireRemote {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(label, "peer-vault:"), "~", 2)
		if len(parts) != 2 {
			return nil, ErrVaultLocked
		}
		peer := s.peerFor(parts[0])
		if peer == nil {
			return nil, ErrVaultLocked
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		resp, e := peerRequest(ctx, *peer, "GET", "/v1/vaults", nil, nil)
		if e != nil {
			cancel()
			return nil, ErrVaultLocked
		}
		var entries []VaultStatus
		e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&entries)
		resp.Body.Close()
		cancel()
		if e != nil || resp.StatusCode != 200 {
			return nil, ErrVaultLocked
		}
		unlocked := false
		for _, v := range entries {
			if v.ID == parts[1] && v.Source == parts[0] && v.State == "unlocked" {
				unlocked = true
			}
		}
		if !unlocked {
			return nil, ErrVaultLocked
		}
	}
	if s.vaults == nil {
		return nil, ErrVaultLocked
	}
	return s.vaults.recordCipher(local)
}

func (s *Service) federatedProtection(local http.Handler, w http.ResponseWriter, r *http.Request) {
	// Never keep an unlock key in a durable record or log it.
	data, e := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if e != nil || len(data) > 1<<20 {
		fail(w, ErrUnsafe)
		return
	}
	defer clear(data)
	r.Body = io.NopCloser(bytes.NewReader(data))
	rec := httptest.NewRecorder()
	local.ServeHTTP(rec, r)
	if rec.Code != 200 {
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		return
	}
	var status map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &status)
	if r.Method == "GET" {
		reply(w, 200, status)
		return
	}
	peerStates := map[string]string{}
	for _, p := range s.config.Peers {
		timeout := 60 * time.Second
		if strings.HasSuffix(r.URL.Path, "/unlock") {
			timeout = 2 * time.Second
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		resp, err := peerRequest(ctx, p, r.Method, r.URL.Path, r.URL.Query(), bytes.NewReader(data))
		if err != nil {
			cancel()
			if strings.HasSuffix(r.URL.Path, "/lock") {
				fail(w, ErrMount)
				return
			}
			peerStates[p.ID] = "unavailable"
			continue
		}
		var remote map[string]any
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&remote)
		resp.Body.Close()
		cancel()
		if err != nil || resp.StatusCode != 200 {
			if strings.HasSuffix(r.URL.Path, "/lock") {
				fail(w, ErrVaultLocked)
				return
			}
			peerStates[p.ID] = "unavailable"
			continue
		}
		peerStates[p.ID] = "ready"
		if r.Method == "GET" && status["state"] != remote["state"] {
			status["state"] = "error"
		}
	}
	status["peers"] = peerStates
	reply(w, 200, status)
}
