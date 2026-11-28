package service

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestArchiveLegacyVaultLabelsFollowRemoteOwnershipWithoutRewritingAAD(t *testing.T) {
	s := testVaultService()
	id := strings.Repeat("a", 32)
	var unlocked atomic.Bool
	unlocked.Store(true)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/vaults" {
			http.NotFound(w, r)
			return
		}
		state := "locked"
		if unlocked.Load() {
			state = "unlocked"
		}
		reply(w, 200, []VaultStatus{{ID: id, Source: "Remote", State: state}})
	}))
	defer server.Close()
	peer := testPeer(server, "remote", "Remote", strings.Repeat("t", 32))
	peer.VaultSources = map[string]string{id: "Remote"}
	s.config.Peers = []PeerConfig{peer}
	labels := []string{"Protected", "vault:" + id}
	cipher, e := s.vaults.recordCipher([]string{"Protected"})
	if e != nil {
		t.Fatal(e)
	}
	nonce := make([]byte, cipher.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		t.Fatal(e)
	}
	plain := []byte(`{"library":{"source":"Protected","path":"private/family"},"archive":{"source":"Remote","vault":"` + id + `","path":"private/originals"}}`)
	sealed := sealedRecord{Version: 1, ID: "link", Sources: labels, Nonce: nonce, Ciphertext: cipher.Seal(nil, nonce, plain, recordAAD("link", "archiveLink", labels))}
	raw, _ := json.Marshal(sealed)
	decoded, e := s.decodeRecord("link", "archiveLink", raw)
	if e != nil || !bytes.Equal(decoded, plain) {
		t.Fatal("legacy ciphertext lost", e)
	}
	unlocked.Store(false)
	if _, e = s.decodeRecord("link", "archiveLink", raw); !errors.Is(e, ErrVaultLocked) {
		t.Fatal("remote lock bypassed", e)
	}
}
func TestArchiveMirroredPeerLabelsRequireTheNowLocalVaultUnlocked(t *testing.T) {
	s := testVaultService()
	cipher, e := s.nodeRecordCipher([]string{"peer-vault:Protected~archive"}, true)
	if e != nil {
		t.Fatal("local mirror ownership", e)
	}
	if cipher == nil {
		t.Fatal("missing metadata cipher")
	}
	s.vaults.states[0].unlocked = false
	if _, e = s.nodeRecordCipher([]string{"peer-vault:Protected~archive"}, true); !errors.Is(e, ErrVaultLocked) {
		t.Fatal("local lock bypassed", e)
	}
}
