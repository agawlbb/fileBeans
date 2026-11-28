package service

import (
	"bytes"
	"encoding/json"
	"golang.org/x/sys/unix"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func syncBroker(t *testing.T, server, peer string) *mountBroker {
	t.Helper()
	b := testBroker(t)
	b.config.ProtectionSync = &BrokerProtectionSyncConfig{Group: "test-group", Server: server, Peer: peer}
	b.consumerReady = time.Now()
	return b
}
func syncBrokerCommand(t *testing.T, b *mountBroker, cmd protectionSyncCommand) (*httptest.ResponseRecorder, protectionSyncSnapshot) {
	t.Helper()
	if cmd.Action == "unlock" && cmd.TargetBoot == "" {
		if err := b.protectionSyncInit(); err != nil {
			t.Fatal(err)
		}
		cmd.TargetBoot = b.protectionSync.boot
	}
	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	out := brokerRequest(b, &b.config.ControlUID, "POST", "/v1/protection-session", string(data))
	var result protectionSyncSnapshot
	if out.Code == 200 && json.Unmarshal(out.Body.Bytes(), &result) != nil {
		t.Fatal(out.Body.String())
	}
	return out, result
}
func TestProtectionSyncBrokerCommandsFencingAndNoKeyPersistence(t *testing.T) {
	b := syncBroker(t, "one", "two")
	code := recoveryCode(bytes.Repeat([]byte{0x42}, 32))
	unlockID := ID()
	out, snap := syncBrokerCommand(t, b, protectionSyncCommand{Action: "unlock", ID: unlockID, Origin: "one", Key: code, Epochs: map[string]uint64{"one": 0, "two": 0}})
	if out.Code != 200 || snap.State != "unlocked" {
		t.Fatal(out.Code, out.Body.String())
	}
	out, locked := syncBrokerCommand(t, b, protectionSyncCommand{Action: "lock", ID: ID(), Origin: "one", Epochs: snap.Epochs})
	if out.Code != 200 || locked.State != "locked" || locked.Epochs["one"] != 1 {
		t.Fatal(out.Code, out.Body.String())
	}
	out, _ = syncBrokerCommand(t, b, protectionSyncCommand{Action: "unlock", ID: ID(), Origin: "two", Key: code, Epochs: snap.Epochs})
	if out.Code != 409 {
		t.Fatal("stale unlock crossed newer lock", out.Code, out.Body.String())
	}
	out, again := syncBrokerCommand(t, b, protectionSyncCommand{Action: "unlock", ID: unlockID, Origin: "one", Key: code, Epochs: locked.Epochs})
	if out.Code != 200 || again.State != "locked" {
		t.Fatal("duplicate unlocked after lock", out.Code, out.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(b.config.State, "protection-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(code)) || bytes.Contains(data, bytes.Repeat([]byte{0x42}, 32)) || bytes.Contains(data, []byte("key")) {
		t.Fatal("unlock material persisted")
	}
	prior := b.protectionSync.boot
	b.protectionSync = nil
	if err = b.protectionSyncInit(); err != nil {
		t.Fatal(err)
	}
	if b.protectionSync.disk.Epochs["one"] != 1 || b.protectionSync.boot == prior || b.protectionSync.local.IsZero() == false {
		t.Fatal("restart lost fencing or retained activity")
	}
}
func TestProtectionSyncActivityNoEchoOrHeartbeatExtension(t *testing.T) {
	b := syncBroker(t, "one", "two")
	if err := b.protectionSyncInit(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	b.protectionSyncActivity(start, start)
	remote := protectionSyncSnapshot{Group: "test-group", Server: "two", State: "unlocked", Boot: ID(), Sequence: 1, AgeMillis: 0, Epochs: map[string]uint64{"one": 0, "two": 0}}
	later := start.Add(50 * time.Minute)
	if err := b.protectionSyncImport(&remote, later); err != nil {
		t.Fatal(err)
	}
	if got := b.protectionSyncActivity(start, start.Add(time.Hour)); !got.Equal(later) {
		t.Fatal("remote activity did not extend all folders")
	}
	snapshot := b.protectionSyncSnapshot(start.Add(time.Hour))
	if snapshot.Sequence != 1 || snapshot.AgeMillis != time.Hour.Milliseconds() {
		t.Fatal("remote activity echoed as local", snapshot)
	}
	if err := b.protectionSyncImport(&remote, later.Add(59*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !b.protectionSync.remote.Equal(later) {
		t.Fatal("duplicate heartbeat slid deadline")
	}
	if later.Add(b.protectionIdleTimeout()).Sub(b.protectionSyncActivity(start, later.Add(b.protectionIdleTimeout()))) < b.protectionIdleTimeout() {
		t.Fatal("shared deadline did not expire")
	}
	remote.Sequence++
	remote.AgeMillis = time.Hour.Milliseconds() + 1
	if err := b.protectionSyncImport(&remote, later.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !b.protectionSync.remote.Equal(later) {
		t.Fatal("stale remote activity extended session")
	}
}
func TestProtectionSyncMissedLockSurvivesRestartAndActivityCannotUnlock(t *testing.T) {
	b := syncBroker(t, "one", "two")
	if err := b.protectionSyncInit(); err != nil {
		t.Fatal(err)
	}
	remote := protectionSyncSnapshot{Group: "test-group", Server: "two", State: "unlocked", Boot: ID(), Sequence: 3, Epochs: map[string]uint64{"one": 0, "two": 4}}
	if err := b.protectionSyncImport(&remote, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !b.protectionSync.disk.Pending || b.protectionStatus().State != "locked" {
		t.Fatal("activity unlocked folder or missed lock intent")
	}
	b.protectionSync = nil
	if err := b.protectionSyncInit(); err != nil {
		t.Fatal(err)
	}
	if !b.protectionSync.disk.Pending || b.protectionSync.disk.Epochs["two"] != 4 {
		t.Fatal("pending lock was not durable")
	}
	b.pollIdleVaults(map[string]*vaultIdleWatch{}, time.Now())
	if b.protectionSync.disk.Pending {
		t.Fatal("pending lock did not settle")
	}
}
func TestProtectionSyncBrokerAuthorization(t *testing.T) {
	b := syncBroker(t, "one", "two")
	out := brokerRequest(b, &b.config.ReaderUID, "POST", "/v1/protection-session", `{"action":"activity"}`)
	if out.Code != 403 {
		t.Fatal("reader changed protection", out.Code)
	}
	out, _ = syncBrokerCommand(t, b, protectionSyncCommand{Action: "activity", Remote: &protectionSyncSnapshot{Group: "other", Server: "two", Boot: strings.Repeat("a", 32)}})
	if out.Code == 200 {
		t.Fatal("foreign group accepted")
	}
}

func TestProtectionSyncUnlockStartsFreshIdleIntervalWithOldWatches(t *testing.T) {
	b := syncBroker(t, "one", "two")
	if err := b.protectionSyncInit(); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	b.protectionSync.local = now
	if got := b.protectionSyncActivity(now.Add(-2*time.Hour), now); !got.Equal(now) {
		t.Fatal("old watches overrode fresh unlock", got)
	}
}

func TestProtectionSyncLockWinsWhileUnlockWaitsForLease(t *testing.T) {
	b := syncBroker(t, "one", "two")
	if err := b.protectionSyncInit(); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(filepath.Join(b.config.Runtime, "gate"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err = unix.Flock(int(held.Fd()), unix.LOCK_SH); err != nil {
		t.Fatal(err)
	}
	start := func(cmd protectionSyncCommand) <-chan *httptest.ResponseRecorder {
		if cmd.Action == "unlock" {
			cmd.TargetBoot = b.protectionSync.boot
		}
		ch := make(chan *httptest.ResponseRecorder, 1)
		data, _ := json.Marshal(cmd)
		go func() { ch <- brokerRequest(b, &b.config.ControlUID, "POST", "/v1/protection-session", string(data)) }()
		return ch
	}
	code := recoveryCode(bytes.Repeat([]byte{0x42}, 32))
	unlocked := start(protectionSyncCommand{Action: "unlock", ID: ID(), Origin: "two", Key: code, Epochs: map[string]uint64{"one": 0, "two": 0}})
	time.Sleep(25 * time.Millisecond)
	locked := start(protectionSyncCommand{Action: "lock", ID: ID(), Origin: "one"})
	deadline := time.Now().Add(time.Second)
	for {
		b.mu.Lock()
		generation := b.protectionSync.disk.Epochs["one"]
		b.mu.Unlock()
		if generation == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lock did not fence before waiting for lease")
		}
		time.Sleep(time.Millisecond)
	}
	held.Close()
	select {
	case out := <-unlocked:
		if out.Code != 409 {
			t.Fatal("overlapping unlock was not fenced", out.Code, out.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unlock did not complete")
	}
	select {
	case out := <-locked:
		if out.Code != 200 {
			t.Fatal(out.Code, out.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lock did not complete")
	}
	if b.protectionStatus().State != "locked" {
		t.Fatal("overlap reopened protection")
	}
}

func TestProtectionSyncRejectsUnboundedEncodedCommandIDs(t *testing.T) {
	b := syncBroker(t, "one", "two")
	out, _ := syncBrokerCommand(t, b, protectionSyncCommand{Action: "lock", ID: strings.Repeat("<", 32), Origin: "one"})
	if out.Code == 200 {
		t.Fatal("non-UUID command ID accepted")
	}
	if _, err := os.Stat(filepath.Join(b.config.State, "protection-session.json")); !os.IsNotExist(err) {
		t.Fatal("invalid command persisted", err)
	}
	out, _ = syncBrokerCommand(t, b, protectionSyncCommand{Action: "lock", ID: ID(), Origin: "one"})
	if out.Code != 200 {
		t.Fatal("valid command failed after rejected input", out.Code, out.Body.String())
	}
}

func TestProtectionSyncRestartRejectsInFlightUnlock(t *testing.T) {
	b := syncBroker(t, "one", "two")
	if err := b.protectionSyncInit(); err != nil {
		t.Fatal(err)
	}
	before := b.protectionSync.boot
	b.protectionSync = nil
	if err := b.protectionSyncInit(); err != nil {
		t.Fatal(err)
	}
	cmd := protectionSyncCommand{Action: "unlock", ID: ID(), Origin: "two", Key: recoveryCode(bytes.Repeat([]byte{0x42}, 32)), TargetBoot: before, Epochs: map[string]uint64{"one": 0, "two": 0}}
	out, _ := syncBrokerCommand(t, b, cmd)
	if out.Code != 409 || b.protectionStatus().State != "locked" {
		t.Fatal("pre-restart unlock opened restarted broker", out.Code, out.Body.String())
	}
}
