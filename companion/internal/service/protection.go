package service

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type protectionStatus struct {
	State string `json:"state"`
}

func (b *mountBroker) protectionStatus() protectionStatus {
	statuses := b.statuses()
	opened := 0
	total := 0
	for _, v := range statuses {
		if !v.Initialized {
			continue
		}
		total++
		if v.State == "error" {
			return protectionStatus{"error"}
		}
		if v.State == "unlocked" {
			opened++
		}
	}
	if opened == 0 {
		if total == 0 && len(b.locationKey) > 0 {
			return protectionStatus{"unlocked"}
		}
		return protectionStatus{"locked"}
	}
	if opened == total {
		return protectionStatus{"unlocked"}
	}
	return protectionStatus{"error"}
}
func (b *mountBroker) lockAll() error {
	var result error
	// Keep the location key until the last private mount has closed.
	ids := make([]string, 0, len(b.registry.Entries))
	for _, e := range b.registry.Entries {
		ids = append(ids, e.ID)
	}
	for _, id := range ids {
		for _, e := range b.registry.Entries {
			if e.ID == id {
				if err := b.lock(e); err != nil {
					result = errors.Join(result, err)
				}
				break
			}
		}
	}
	b.forgetClosedLocations()
	return result
}
func (b *mountBroker) unlockAll(key []byte) error {
	if time.Since(b.consumerReady) >= 15*time.Second {
		return errVault
	}
	if err := b.activateLocations(key); err != nil {
		b.forgetClosedLocations()
		return err
	}
	entries := append([]brokerEntry(nil), b.registry.Entries...)
	for _, e := range entries {
		if !e.Initialized {
			continue
		}
		if err := b.open(e, key); err != nil {
			_ = b.lockAll()
			return err
		}
	}
	return b.publish()
}

// Waiting must never hold b.mu: active consumers may need broker requests in
// order to finish their I/O and release the shared gate.
func (b *mountBroker) exclusiveContext(ctx context.Context) (*os.File, error) {
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		gate, err := b.exclusive()
		if err == nil {
			return gate, nil
		}
		if !errors.Is(err, ErrVaultBusy) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			log.Print("Protection transition timed out waiting for filesystem leases")
			return nil, ErrVaultBusy
		case <-ticker.C:
		}
	}
}
func protectionAction(path string) string {
	if strings.HasSuffix(path, "/unlock") {
		return "unlock"
	}
	if strings.HasSuffix(path, "/lock") {
		return "lock"
	}
	return ""
}
func (b *mountBroker) globalProtection(w http.ResponseWriter, r *http.Request) {
	action := protectionAction(r.URL.Path)
	var input struct {
		Key string `json:"key"`
	}
	var key []byte
	if action == "unlock" {
		if err := body(r, &input); err != nil {
			brokerFail(w, err)
			return
		}
		var err error
		key, err = parseVaultKey(input.Key)
		input.Key = ""
		if err != nil {
			brokerFail(w, err)
			return
		}
		defer clear(key)
	}
	gate, err := b.exclusiveContext(r.Context())
	if err != nil {
		brokerFail(w, err)
		return
	}
	defer gate.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	if strings.Contains(r.URL.Path, "/vaults/") {
		id := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/vaults/"), "/")[0]
		found := false
		for _, e := range b.registry.Entries {
			if e.ID == id {
				found = true
				break
			}
		}
		if !found {
			brokerFail(w, ErrUnsafe)
			return
		}
	}
	if action == "lock" {
		err = b.lockAll()
	} else {
		err = b.unlockAll(key)
	}
	if err != nil {
		log.Print("Protection transition could not complete filesystem mount changes")
		brokerFail(w, err)
		return
	}
	reply(w, 200, b.protectionStatus())
}
func (s *Service) protectionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/protection", func(w http.ResponseWriter, r *http.Request) {
		if s.vaults == nil || s.vaults.remote == nil {
			fail(w, ErrUnsupported)
			return
		}
		var status protectionStatus
		if err := s.vaults.remote.request("GET", "protection", nil, "", &status); err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, status)
	})
	for _, action := range []string{"lock", "unlock"} {
		mux.HandleFunc("POST /v1/protection/"+action, func(w http.ResponseWriter, r *http.Request) {
			if s.vaults == nil || s.vaults.remote == nil {
				fail(w, ErrUnsupported)
				return
			}
			var input struct {
				Key string `json:"key"`
			}
			if action == "unlock" {
				if err := body(r, &input); err != nil {
					fail(w, err)
					return
				}
			}
			defer func() { input.Key = "" }()
			var result protectionStatus
			if err := s.vaults.remote.request("POST", "protection/"+action, input, "", &result); err != nil {
				fail(w, err)
				return
			}
			if action == "unlock" {
				if err := s.migrateVaultRecords(); err != nil {
					fail(w, err)
					return
				}
			}
			reply(w, 200, result)
		})
	}
}
