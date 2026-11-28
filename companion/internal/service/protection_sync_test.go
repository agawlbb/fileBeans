package service

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func protectionPair(t *testing.T) (*Service, *Service, *mountBroker, *mountBroker) {
	t.Helper()
	a := syncBroker(t, "one", "two")
	b := syncBroker(t, "two", "one")
	node := func(id, peer string, broker *mountBroker) *Service {
		s := &Service{config: Config{ServerID: id, State: t.TempDir(), Token: strings.Repeat("t", 32), ProtectionSync: &ProtectionSyncConfig{Group: "test-group", Name: id, Peer: peer, PeerName: peer, Key: strings.Repeat("s", 32)}}}
		s.protectionLocalTest = func(cmd *protectionSyncCommand) (protectionSyncSnapshot, error) {
			value := protectionSyncCommand{}
			if cmd != nil {
				value = *cmd
			}
			data, _ := json.Marshal(value)
			out := brokerRequest(broker, &broker.config.ControlUID, "POST", "/v1/protection-session", string(data))
			var result protectionSyncSnapshot
			if out.Code != 200 {
				if out.Code == 409 {
					return result, ErrConflict
				}
				return result, ErrVaultKey
			}
			err := json.Unmarshal(out.Body.Bytes(), &result)
			return result, err
		}
		return s
	}
	one, two := node("one", "two", a), node("two", "one", b)
	left, right := httptest.NewTLSServer(one.Handler()), httptest.NewTLSServer(two.Handler())
	t.Cleanup(left.Close)
	t.Cleanup(right.Close)
	one.config.Peers = []PeerConfig{testPeer(right, "two", "disk", two.config.Token)}
	two.config.Peers = []PeerConfig{testPeer(left, "one", "disk", one.config.Token)}
	return one, two, a, b
}
func protectionActionRequest(t *testing.T, s *Service, action, code, id string) protectionGroupStatus {
	t.Helper()
	data, _ := json.Marshal(map[string]string{"key": code})
	req := httptest.NewRequest("POST", "/v1/protection/"+action, bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+s.config.Token)
	if id != "" {
		req.Header.Set("Idempotency-Key", id)
	}
	out := httptest.NewRecorder()
	s.Handler().ServeHTTP(out, req)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var status protectionGroupStatus
	if err := json.Unmarshal(out.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	return status
}
func TestProtectionSyncCommandsFromEitherServer(t *testing.T) {
	one, two, a, b := protectionPair(t)
	code := recoveryCode(bytes.Repeat([]byte{0x42}, 32))
	if status := protectionActionRequest(t, one, "unlock", code, ID()); status.GroupState != "unlocked" {
		t.Fatal(status)
	}
	lockID := ID()
	if status := protectionActionRequest(t, two, "lock", "", lockID); status.GroupState != "locked" {
		t.Fatal(status)
	}
	epochs := cloneEpochs(b.protectionSync.disk.Epochs)
	protectionActionRequest(t, two, "lock", "", lockID)
	if !sameEpochs(epochs, b.protectionSync.disk.Epochs) {
		t.Fatal("duplicate command advanced lock generation")
	}
	if status := protectionActionRequest(t, two, "unlock", code, ID()); status.GroupState != "unlocked" {
		t.Fatal(status)
	}
	if a.protectionStatus().State != "unlocked" || b.protectionStatus().State != "unlocked" {
		t.Fatal("peer did not open")
	}
	// User file activity crosses in both directions without any client heartbeat.
	now := time.Now()
	a.protectionSyncActivity(now, now)
	one.protectionExchange(t.Context())
	if b.protectionSync.remote.IsZero() {
		t.Fatal("activity did not reach peer")
	}
	b.protectionSyncActivity(now.Add(time.Minute), now.Add(time.Minute))
	two.protectionExchange(t.Context())
	if a.protectionSync.remote.IsZero() {
		t.Fatal("reverse activity did not reach peer")
	}
}
func TestProtectionSyncOfflineUnlockIsLocalAndNeverReplayed(t *testing.T) {
	one, _, a, b := protectionPair(t)
	original := one.config.Peers[0].URL
	one.config.Peers[0].URL = "https://127.0.0.1:1"
	code := recoveryCode(bytes.Repeat([]byte{0x42}, 32))
	status := protectionActionRequest(t, one, "unlock", code, ID())
	if status.State != "unlocked" || status.GroupState != "partial" || status.Members[1].State != "unavailable" {
		t.Fatal(status)
	}
	if b.protectionStatus().State != "locked" {
		t.Fatal("unreachable peer opened")
	}
	one.config.Peers[0].URL = original
	one.protectionExchange(t.Context())
	if b.protectionStatus().State != "locked" || a.protectionStatus().State != "unlocked" {
		t.Fatal("reconnect replayed unlock or blocked local access")
	}
	status = protectionActionRequest(t, one, "unlock", code, ID())
	if status.GroupState != "unlocked" {
		t.Fatal(status)
	}
	one.config.Peers[0].URL = "https://127.0.0.1:1"
	status = protectionActionRequest(t, one, "lock", "", ID())
	if status.State != "locked" || b.protectionStatus().State != "unlocked" {
		t.Fatal("offline lock did not preserve local behavior", status)
	}
	one.config.Peers[0].URL = original
	one.protectionExchange(t.Context())
	if !b.protectionSync.disk.Pending {
		t.Fatal("missed lock not propagated")
	}
	b.pollIdleVaults(map[string]*vaultIdleWatch{}, time.Now())
	if b.protectionStatus().State != "locked" {
		t.Fatal("missed lock did not settle")
	}
}
func TestProtectionSyncPeerCredentialAndGroupBoundaries(t *testing.T) {
	one, _, _, _ := protectionPair(t)
	for _, tc := range []struct{ credential, group, from string }{
		{one.config.Token, "test-group", "two"}, {strings.Repeat("s", 32), "other", "two"}, {strings.Repeat("s", 32), "test-group", "stranger"},
	} {
		data, _ := json.Marshal(protectionPeerMessage{Group: tc.group, From: tc.from})
		req := httptest.NewRequest("POST", "/v1/peer/protection", bytes.NewReader(data))
		req.Header.Set("X-FileBeans-Protection-Key", tc.credential)
		out := httptest.NewRecorder()
		one.Handler().ServeHTTP(out, req)
		if out.Code == 200 {
			t.Fatal("unauthorized session access", tc)
		}
	}
}
func TestProtectionSyncConfigurationRequiresSeparateCredential(t *testing.T) {
	one, _, _, _ := protectionPair(t)
	if err := one.config.validateProtectionSync(); err != nil {
		t.Fatal(err)
	}
	one.config.ProtectionSync.Key = one.config.Token
	if err := one.config.validateProtectionSync(); err == nil {
		t.Fatal("client token accepted as coordination key")
	}
}
func TestProtectionSyncFailedLocalTransitionDoesNotPreventPeerLock(t *testing.T) {
	one, two, _, b := protectionPair(t)
	code := recoveryCode(bytes.Repeat([]byte{0x42}, 32))
	protectionActionRequest(t, two, "unlock", code, ID())
	original := one.protectionLocalTest
	one.protectionLocalTest = func(cmd *protectionSyncCommand) (protectionSyncSnapshot, error) {
		if cmd != nil && cmd.Action == "lock" {
			return protectionSyncSnapshot{}, ErrVaultBusy
		}
		return original(cmd)
	}
	status := protectionActionRequest(t, one, "lock", "", ID())
	if status.GroupState != "partial" || b.protectionStatus().State != "locked" {
		t.Fatal("local busy blocked reachable peer lock", status)
	}
}

func TestProtectionSyncPeerIdleClosureDoesNotLockActiveMember(t *testing.T) {
	one, _, a, b := protectionPair(t)
	code := recoveryCode(bytes.Repeat([]byte{0x42}, 32))
	protectionActionRequest(t, one, "unlock", code, ID())
	// A local idle closure does not create explicit cross-server lock intent.
	b.mu.Lock()
	err := b.lockAll()
	b.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	one.protectionExchange(t.Context())
	a.pollIdleVaults(map[string]*vaultIdleWatch{}, time.Now())
	if a.protectionStatus().State != "unlocked" || b.protectionStatus().State != "locked" {
		t.Fatal("reconnect changed independent states without a user command")
	}
}

func TestProtectionSyncRetriedPublicUnlockCannotCrossNewerLock(t *testing.T) {
	one, _, a, b := protectionPair(t)
	url := one.config.Peers[0].URL
	one.config.Peers[0].URL = "https://127.0.0.1:1"
	code := recoveryCode(bytes.Repeat([]byte{0x42}, 32))
	original := ID()
	protectionActionRequest(t, one, "unlock", code, original)
	protectionActionRequest(t, one, "lock", "", ID())
	one.config.Peers[0].URL = url
	one.protectionExchange(t.Context())
	b.pollIdleVaults(map[string]*vaultIdleWatch{}, time.Now())
	status := protectionActionRequest(t, one, "unlock", code, original)
	if a.protectionStatus().State != "locked" || b.protectionStatus().State != "locked" {
		t.Fatal("transport retry refreshed an old unlock generation", status)
	}
}
func TestProtectionSyncReceiptCannotRetargetRestartedPeer(t *testing.T) {
	one, _, _, b := protectionPair(t)
	code := recoveryCode(bytes.Repeat([]byte{0x42}, 32))
	original := ID()
	protectionActionRequest(t, one, "unlock", code, original)
	b.mu.Lock()
	err := b.lockAll()
	b.protectionSync = nil
	b.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	status := protectionActionRequest(t, one, "unlock", code, original)
	if b.protectionStatus().State != "locked" {
		t.Fatal("retry retargeted a restarted peer", status)
	}
}
