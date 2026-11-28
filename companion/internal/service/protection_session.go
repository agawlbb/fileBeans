package service

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type BrokerProtectionSyncConfig struct {
	Group  string `json:"group"`
	Server string `json:"server"`
	Peer   string `json:"peer"`
}
type protectionSyncCommand struct {
	Action string                  `json:"action,omitempty"`
	ID     string                  `json:"id,omitempty"`
	Origin string                  `json:"origin,omitempty"`
	Key    string                  `json:"key,omitempty"`
	Epochs map[string]uint64       `json:"epochs,omitempty"`
	Remote *protectionSyncSnapshot `json:"remote,omitempty"`
}
type protectionSyncSnapshot struct {
	SessionAgeMillis int64             `json:"sessionAgeMillis"`
	State            string            `json:"state"`
	Group            string            `json:"group"`
	Server           string            `json:"server"`
	Boot             string            `json:"boot"`
	Sequence         uint64            `json:"sequence"`
	AgeMillis        int64             `json:"ageMillis"`
	Epochs           map[string]uint64 `json:"epochs"`
	Pending          bool              `json:"pending"`
	Error            string            `json:"error,omitempty"`
}
type protectionSyncDisk struct {
	Group        string            `json:"group"`
	Epochs       map[string]uint64 `json:"epochs"`
	Acknowledged map[string]uint64 `json:"acknowledged"`
	Seen         []string          `json:"seen,omitempty"`
	Pending      bool              `json:"pending"`
}
type brokerProtectionSync struct {
	disk           protectionSyncDisk
	boot           string
	sequence       uint64
	local          time.Time
	remote         time.Time
	remoteBoot     string
	remoteSequence uint64
}

func cloneEpochs(v map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(v))
	for k, n := range v {
		out[k] = n
	}
	return out
}
func mergeEpochs(a, b map[string]uint64) map[string]uint64 {
	out := cloneEpochs(a)
	for k, n := range b {
		if n > out[k] {
			out[k] = n
		}
	}
	return out
}
func sameEpochs(a, b map[string]uint64) bool {
	for k, n := range a {
		if b[k] != n {
			return false
		}
	}
	for k, n := range b {
		if a[k] != n {
			return false
		}
	}
	return true
}
func (b *mountBroker) protectionSyncInit() error {
	c := b.config.ProtectionSync
	if c == nil {
		return ErrUnsupported
	}
	if c.Group == "" || !nodeIDPattern.MatchString(c.Group) || !nodeIDPattern.MatchString(c.Server) || !nodeIDPattern.MatchString(c.Peer) || c.Server == c.Peer {
		return ErrUnsafe
	}
	if b.protectionSync != nil {
		return nil
	}
	d := protectionSyncDisk{Group: c.Group, Epochs: map[string]uint64{c.Server: 0, c.Peer: 0}, Acknowledged: map[string]uint64{}}
	f, err := os.Open(filepath.Join(b.config.State, "protection-session.json"))
	if err == nil {
		defer f.Close()
		err = json.NewDecoder(io.LimitReader(f, 16384)).Decode(&d)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if d.Group != c.Group || d.Epochs == nil || d.Acknowledged == nil || len(d.Seen) > 128 {
		return ErrUnsafe
	}
	for k := range d.Epochs {
		if k != c.Server && k != c.Peer {
			return ErrUnsafe
		}
	}
	b.protectionSync = &brokerProtectionSync{disk: d, boot: ID()}
	return nil
}

// Only command IDs and lock generations are durable. No recovery key or pending
// unlock is written here. Publish the new state only after an atomic fsync.
func (b *mountBroker) protectionSyncPersist(next protectionSyncDisk) error {
	data, err := json.Marshal(next)
	if len(data) > 16384 {
		return ErrUnsafe
	}
	if err != nil {
		return err
	}
	temp := filepath.Join(b.config.State, ".protection-session-"+ID())
	f, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(temp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(temp, filepath.Join(b.config.State, "protection-session.json")); err != nil {
		return err
	}
	dir, err := os.Open(b.config.State)
	if err != nil {
		return err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return err
	}
	b.protectionSync.disk = next
	return nil
}
func (b *mountBroker) protectionSyncSeen(id string) bool {
	for _, known := range b.protectionSync.disk.Seen {
		if id == known {
			return true
		}
	}
	return false
}
func (b *mountBroker) protectionSyncRemember(d *protectionSyncDisk, id string) {
	d.Seen = append(append([]string(nil), d.Seen...), id)
	if len(d.Seen) > 128 {
		d.Seen = d.Seen[len(d.Seen)-128:]
	}
}
func (b *mountBroker) protectionSyncLockIntent(id, origin string, epochs map[string]uint64) error {
	if err := b.protectionSyncInit(); err != nil {
		return err
	}
	next := b.protectionSync.disk
	next.Epochs = mergeEpochs(next.Epochs, epochs)
	if origin == b.config.ProtectionSync.Server && !b.protectionSyncSeen("lock:"+id) {
		n := next.Epochs[origin]
		if n == ^uint64(0) {
			return ErrUnsafe
		}
		next.Epochs[origin] = n + 1
	}
	if !b.protectionSyncSeen("lock:" + id) {
		b.protectionSyncRemember(&next, "lock:"+id)
	}
	next.Pending = true
	return b.protectionSyncPersist(next)
}
func (b *mountBroker) protectionSyncSnapshot(now time.Time) protectionSyncSnapshot {
	x := b.protectionSync
	c := b.config.ProtectionSync
	age := int64(vaultIdleTimeout / time.Millisecond)
	if !x.local.IsZero() {
		age = max(0, now.Sub(x.local).Milliseconds())
	}
	latest := x.local
	if x.remote.After(latest) {
		latest = x.remote
	}
	sessionAge := int64(vaultIdleTimeout / time.Millisecond)
	if !latest.IsZero() {
		sessionAge = max(0, now.Sub(latest).Milliseconds())
	}
	return protectionSyncSnapshot{SessionAgeMillis: sessionAge, State: b.protectionStatus().State, Group: c.Group, Server: c.Server, Boot: x.boot, Sequence: x.sequence, AgeMillis: age, Epochs: cloneEpochs(x.disk.Epochs), Pending: x.disk.Pending}
}

// Incoming activity is never re-exported as local activity. Duplicate heartbeats
// cannot slide the idle deadline, and event ages avoid depending on clock sync.
func (b *mountBroker) protectionSyncImport(remote *protectionSyncSnapshot, now time.Time) error {
	c := b.config.ProtectionSync
	if remote == nil || remote.Group != c.Group || remote.Server != c.Peer || !validProtectionID(remote.Boot) || remote.AgeMillis < 0 {
		return ErrUnsafe
	}
	for k := range remote.Epochs {
		if k != c.Server && k != c.Peer {
			return ErrUnsafe
		}
	}
	x := b.protectionSync
	next := x.disk
	next.Epochs = mergeEpochs(next.Epochs, remote.Epochs)
	if !sameEpochs(next.Epochs, x.disk.Epochs) {
		next.Pending = true
		if err := b.protectionSyncPersist(next); err != nil {
			return err
		}
	}
	if remote.State == "unlocked" && !remote.Pending && remote.AgeMillis < int64(vaultIdleTimeout/time.Millisecond) &&
		(remote.Boot != x.remoteBoot || remote.Sequence > x.remoteSequence) {
		x.remoteBoot = remote.Boot
		x.remoteSequence = remote.Sequence
		x.remote = now.Add(-time.Duration(remote.AgeMillis) * time.Millisecond)
	}
	return nil
}
func (b *mountBroker) protectionSyncActivity(local, now time.Time) time.Time {
	if b.config.ProtectionSync == nil {
		return local
	}
	if err := b.protectionSyncInit(); err != nil {
		return local
	}
	x := b.protectionSync
	if local.After(x.local) {
		x.local = local
		x.sequence++
	}
	latest := x.local
	if x.remote.After(latest) {
		latest = x.remote
	}
	return latest
}

// This route owns gate ordering; it must not run beneath the generic broker mutex.
func (b *mountBroker) protectionSyncHTTP(w http.ResponseWriter, r *http.Request) {
	var cmd protectionSyncCommand
	if r.Method != "GET" && r.Method != "POST" {
		brokerFail(w, ErrUnsafe)
		return
	}
	if r.Method == "POST" {
		if err := body(r, &cmd); err != nil {
			brokerFail(w, err)
			return
		}
	}
	defer func() { cmd.Key = "" }()
	b.mu.Lock()
	if err := b.protectionSyncInit(); err != nil {
		b.mu.Unlock()
		brokerFail(w, err)
		return
	}
	if cmd.Action == "" || cmd.Action == "activity" {
		if cmd.Remote != nil {
			if err := b.protectionSyncImport(cmd.Remote, time.Now()); err != nil {
				b.mu.Unlock()
				brokerFail(w, err)
				return
			}
		}
		result := b.protectionSyncSnapshot(time.Now())
		b.mu.Unlock()
		reply(w, 200, result)
		return
	}
	if (cmd.Action != "lock" && cmd.Action != "unlock") || !validProtectionID(cmd.ID) || (cmd.Origin != b.config.ProtectionSync.Server && cmd.Origin != b.config.ProtectionSync.Peer) {
		b.mu.Unlock()
		brokerFail(w, ErrUnsafe)
		return
	}
	for k := range cmd.Epochs {
		if k != b.config.ProtectionSync.Server && k != b.config.ProtectionSync.Peer {
			b.mu.Unlock()
			brokerFail(w, ErrUnsafe)
			return
		}
	}
	if b.protectionSyncSeen(cmd.Action + ":" + cmd.ID) {
		result := b.protectionSyncSnapshot(time.Now())
		b.mu.Unlock()
		reply(w, 200, result)
		return
	}
	if cmd.Action == "lock" {
		if err := b.protectionSyncLockIntent(cmd.ID, cmd.Origin, cmd.Epochs); err != nil {
			b.mu.Unlock()
			brokerFail(w, err)
			return
		}
	}
	b.mu.Unlock()
	gate, err := b.exclusiveContext(r.Context())
	if err != nil {
		brokerFail(w, err)
		return
	}
	defer gate.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	if cmd.Action == "lock" {
		err = b.lockAll()
		if err == nil {
			next := b.protectionSync.disk
			next.Pending = false
			err = b.protectionSyncPersist(next)
		}
	} else {
		if !sameEpochs(cmd.Epochs, b.protectionSync.disk.Epochs) {
			brokerFail(w, ErrConflict)
			return
		}
		key, e := parseVaultKey(cmd.Key)
		cmd.Key = ""
		if e != nil {
			brokerFail(w, e)
			return
		}
		defer clear(key)
		err = b.unlockAll(key)
		if err == nil {
			next := b.protectionSync.disk
			next.Acknowledged = cloneEpochs(next.Epochs)
			next.Pending = false
			b.protectionSyncRemember(&next, "unlock:"+cmd.ID)
			err = b.protectionSyncPersist(next)
			if err != nil {
				_ = b.lockAll()
			} else {
				b.protectionSync.local = time.Now()
				b.protectionSync.sequence++
				b.protectionSync.remote = time.Time{}
			}
		}
	}
	if err != nil {
		brokerFail(w, err)
		return
	}
	reply(w, 200, b.protectionSyncSnapshot(time.Now()))
}

func (b *mountBroker) protectionIdleTimeout() time.Duration {
	if b.config.ProtectionSync != nil {
		return vaultIdleTimeout + 10*time.Second
	}
	return vaultIdleTimeout
}

func validProtectionID(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
