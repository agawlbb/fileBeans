package service

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type ProtectionSyncConfig struct {
	Group    string `json:"group"`
	Name     string `json:"name"`
	Peer     string `json:"peer"`
	PeerName string `json:"peerName"`
	Key      string `json:"key"`
}
type protectionMemberStatus struct {
	Server string `json:"server"`
	Name   string `json:"name"`
	State  string `json:"state"`
	Error  string `json:"error,omitempty"`
}
type protectionGroupStatus struct {
	State      string                   `json:"state"` // Local compatibility for existing clients.
	Group      string                   `json:"group"`
	GroupState string                   `json:"groupState"`
	Members    []protectionMemberStatus `json:"members"`
}
type protectionPeerMessage struct {
	Group   string                `json:"group"`
	From    string                `json:"from"`
	Command protectionSyncCommand `json:"command"`
}

func (c Config) validateProtectionSync() error {
	p := c.ProtectionSync
	if p == nil {
		return nil
	}
	if !nodeIDPattern.MatchString(p.Group) || p.Name == "" || p.PeerName == "" || len(p.Name) > 80 || len(p.PeerName) > 80 || len(p.Key) < 32 || p.Key == c.Token || p.Key == c.ArchivePeerKey {
		return errors.New("invalid protection synchronization configuration")
	}
	for _, peer := range c.Peers {
		if peer.ID == p.Peer && peer.ID != c.ServerID && p.Key != peer.Token && p.Key != peer.ArchiveKey {
			return nil
		}
	}
	return errors.New("protection synchronization requires an existing peer and separate credential")
}
func (s *Service) localProtectionSession(cmd *protectionSyncCommand) (protectionSyncSnapshot, error) {
	if s.protectionLocalTest != nil {
		return s.protectionLocalTest(cmd)
	}
	var result protectionSyncSnapshot
	if s.vaults == nil || s.vaults.remote == nil {
		return result, ErrUnsupported
	}
	method := "GET"
	if cmd != nil {
		method = "POST"
	}
	err := s.vaults.remote.request(method, "protection-session", cmd, "", &result)
	if err == nil && (result.Group != s.config.ProtectionSync.Group || result.Server != s.config.ServerID) {
		err = ErrUnsafe
	}
	return result, err
}
func (s *Service) protectionPeer(ctx context.Context, cmd *protectionSyncCommand) (protectionSyncSnapshot, error) {
	var result protectionSyncSnapshot
	c := s.config.ProtectionSync
	if c == nil {
		return result, ErrUnsupported
	}
	var peer *PeerConfig
	for _, candidate := range s.config.Peers {
		if candidate.ID == c.Peer {
			v := candidate
			peer = &v
			break
		}
	}
	if peer == nil {
		return result, ErrUnsupported
	}
	message := protectionPeerMessage{Group: c.Group, From: s.config.ServerID}
	if cmd != nil {
		message.Command = *cmd
	}
	data, err := json.Marshal(message)
	if err != nil {
		return result, err
	}
	defer clear(data)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(peer.URL, "/")+"/v1/peer/protection", bytes.NewReader(data))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-FileBeans-Protection-Key", c.Key)
	response, err := peerClient(*peer).Do(req)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&failure)
		for _, known := range []error{ErrVaultKey, ErrVaultBusy, ErrConflict, ErrUnsafe} {
			if failure.Error == publicError(known) {
				return result, known
			}
		}
		return result, ErrMount
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&result)
	if err == nil && (result.Group != c.Group || result.Server != c.Peer) {
		err = ErrUnsafe
	}
	return result, err
}
func (s *Service) protectionPeerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/peer/protection", func(w http.ResponseWriter, r *http.Request) {
		c := s.config.ProtectionSync
		if c == nil || len(c.Key) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-FileBeans-Protection-Key")), []byte(c.Key)) != 1 {
			fail(w, ErrAuthorization)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		var msg protectionPeerMessage
		if err := body(r, &msg); err != nil || msg.Group != c.Group || msg.From != c.Peer {
			fail(w, ErrAuthorization)
			return
		}
		if msg.Command.Action != "" && msg.Command.Action != "activity" && msg.Command.Origin != c.Peer {
			fail(w, ErrAuthorization)
			return
		}
		result, err := s.localProtectionSession(&msg.Command)
		msg.Command.Key = ""
		if err != nil {
			fail(w, err)
			return
		}
		if msg.Command.Action == "unlock" && s.db != nil {
			if err := s.migrateVaultRecords(); err != nil {
				result.Error = "Metadata unavailable"
			}
		}
		reply(w, 200, result)
	})
}
func (s *Service) protectionExchange(ctx context.Context) {
	s.protectionSyncMu.Lock()
	defer s.protectionSyncMu.Unlock()
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	remote, err := s.protectionPeer(call, nil)
	cancel()
	if err != nil {
		return
	}
	local, err := s.localProtectionSession(&protectionSyncCommand{Action: "activity", Remote: &remote})
	if err != nil {
		return
	}
	call, cancel = context.WithTimeout(ctx, 2*time.Second)
	_, _ = s.protectionPeer(call, &protectionSyncCommand{Action: "activity", Remote: &local})
	cancel()
}
func (s *Service) protectionSyncLoop(ctx context.Context) {
	if s.config.ProtectionSync == nil {
		return
	}
	for ctx.Err() == nil {
		s.protectionExchange(ctx)
		wait(ctx, 5*time.Second)
	}
}
func (s *Service) groupStatus(local protectionSyncSnapshot, localErr error, remote protectionSyncSnapshot, remoteErr error) protectionGroupStatus {
	c := s.config.ProtectionSync
	result := protectionGroupStatus{State: local.State, Group: c.Group, GroupState: "partial", Members: []protectionMemberStatus{
		{Server: s.config.ServerID, Name: c.Name, State: local.State}, {Server: c.Peer, Name: c.PeerName, State: remote.State}}}
	if localErr != nil {
		result.State = "error"
		result.Members[0].State = "unavailable"
		result.Members[0].Error = publicError(localErr)
	}
	if remoteErr != nil {
		result.Members[1].State = "unavailable"
		result.Members[1].Error = "Unavailable"
		for _, known := range []error{ErrVaultKey, ErrVaultBusy, ErrConflict, ErrUnsafe} {
			if errors.Is(remoteErr, known) {
				result.Members[1].Error = publicError(known)
			}
		}
	}
	if local.Error != "" {
		result.Members[0].Error = local.Error
	}
	if remote.Error != "" {
		result.Members[1].Error = remote.Error
	}
	if local.Pending {
		result.Members[0].Error = "Lock pending"
	}
	if remote.Pending {
		result.Members[1].Error = "Lock pending"
	}
	if localErr == nil && remoteErr == nil && local.Error == "" && remote.Error == "" && !local.Pending && !remote.Pending && local.State == remote.State && (local.State == "locked" || local.State == "unlocked") {
		result.GroupState = local.State
	}
	return result
}
func (s *Service) protectionGroupHTTP(w http.ResponseWriter, r *http.Request, action string) {
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
	if action != "" {
		s.protectionSyncMu.Lock()
		defer s.protectionSyncMu.Unlock()
	}
	local, localErr := s.localProtectionSession(nil)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	remote, remoteErr := s.protectionPeer(ctx, nil)
	cancel()
	if action == "" {
		reply(w, 200, s.groupStatus(local, localErr, remote, remoteErr))
		return
	}
	if remoteErr == nil && localErr == nil {
		local, localErr = s.localProtectionSession(&protectionSyncCommand{Action: "activity", Remote: &remote})
		if localErr == nil {
			ctx, cancel = context.WithTimeout(r.Context(), 2*time.Second)
			remote, remoteErr = s.protectionPeer(ctx, &protectionSyncCommand{Action: "activity", Remote: &local})
			cancel()
		}
	}
	commandID := key(r)
	if !validProtectionID(commandID) {
		commandID = ID()
	}
	cmd := protectionSyncCommand{TargetBoot: local.Boot, Action: action, ID: commandID, Origin: s.config.ServerID, Key: input.Key, Epochs: mergeEpochs(local.Epochs, remote.Epochs)}
	originalPeerBoot := remote.Boot
	if action == "unlock" {
		receipt, err := s.protectionUnlockReceipt(commandID, local, remote)
		if err != nil {
			fail(w, err)
			return
		}
		cmd.Epochs = receipt.Epochs
		cmd.TargetBoot = receipt.LocalBoot
		originalPeerBoot = receipt.PeerBoot
	}

	// Local transition remains available even if the peer is unreachable.
	local, localErr = s.localProtectionSession(&cmd)
	if action == "lock" && localErr == nil {
		cmd.Epochs = local.Epochs
	}
	if remoteErr == nil {
		cmd.TargetBoot = originalPeerBoot
		ctx, cancel = context.WithTimeout(r.Context(), 45*time.Second)
		remote, remoteErr = s.protectionPeer(ctx, &cmd)
		cancel()
	}
	cmd.Key = ""
	input.Key = ""
	if localErr != nil && remoteErr != nil {
		fail(w, localErr)
		return
	}
	if action == "unlock" && localErr == nil && s.db != nil {
		if err := s.migrateVaultRecords(); err != nil {
			local.Error = "Metadata unavailable"
		}
	}
	result := s.groupStatus(local, localErr, remote, remoteErr)
	if local.Error != "" {
		result.Members[0].Error = local.Error
		result.GroupState = "partial"
	}
	if strings.HasPrefix(r.URL.Path, "/v1/vaults/") {
		reply(w, 200, s.vaults.status())
		return
	}
	reply(w, 200, result)
}
