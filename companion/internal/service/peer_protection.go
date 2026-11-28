package service

import (
	"context"
	"crypto/cipher"
	"encoding/json"
	"io"
	"strings"
	"time"
)

// Remote protected names are encrypted by the destination broker's volatile
// metadata key. Reading them additionally requires the source vault unlocked.
func (s *Service) nodeRecordCipher(sources []string, requireRemote bool) (cipher.AEAD, error) {
	local := []string{}
	for _, label := range sources {
		// Legacy archive ciphertext keeps its original AAD and public vault IDs.
		// Ownership changes must not decrypt/re-encrypt records during handoff.
		if strings.HasPrefix(label, "vault:") {
			for _, p := range s.config.Peers {
				if source, ok := p.VaultSources[strings.TrimPrefix(label, "vault:")]; ok {
					label = "peer-vault:" + source + "~" + strings.TrimPrefix(label, "vault:")
					break
				}
			}
		}
		if !strings.HasPrefix(label, "peer-vault:") {
			local = append(local, label)
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(label, "peer-vault:"), "~", 2)
		if len(parts) != 2 {
			return nil, ErrVaultLocked
		}
		if owner, source, qualified := splitPeerSource(parts[0]); qualified && owner == s.config.ServerID {
			parts[0] = source
		}
		peer := s.peerFor(parts[0])
		if peer == nil {
			found := false
			if s.vaults != nil {
				for _, v := range s.vaults.configs() {
					if v.Source == parts[0] && v.ID == parts[1] {
						if s.vaults.remote != nil {
							local = append(local, "vault:"+v.ID)
						} else {
							local = append(local, v.Source)
						}
						found = true
						break
					}
				}
			}
			if !found {
				return nil, ErrVaultLocked
			}
			continue
		}
		if !requireRemote {
			continue
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
			if v.ID == parts[1] && v.Source == peerSourceFor(parts[0], peer.ID) && v.State == "unlocked" {
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
