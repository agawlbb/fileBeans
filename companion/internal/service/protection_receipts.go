package service

// Receipts pin a caller's command ID to its original lock generations and boot
// identities. They contain no key or executable/queued unlock operation.
import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

type protectionReceipt struct {
	Created   int64             `json:"created"`
	Epochs    map[string]uint64 `json:"epochs"`
	LocalBoot string            `json:"localBoot"`
	PeerBoot  string            `json:"peerBoot"`
}

func (s *Service) protectionUnlockReceipt(id string, local, remote protectionSyncSnapshot) (protectionReceipt, error) {
	root := s.config.State
	path := filepath.Join(root, "protection-receipts.json")
	records := map[string]protectionReceipt{}
	f, err := os.Open(path)
	if err == nil {
		err = json.NewDecoder(io.LimitReader(f, 256<<10)).Decode(&records)
		f.Close()
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return protectionReceipt{}, err
	}
	if existing, ok := records[id]; ok {
		return existing, nil
	}
	now := time.Now().Unix()
	for key, record := range records {
		if now-record.Created > 24*60*60 {
			delete(records, key)
		}
	}
	if len(records) >= 256 {
		return protectionReceipt{}, ErrVaultBusy
	}
	result := protectionReceipt{Created: now, Epochs: mergeEpochs(local.Epochs, remote.Epochs), LocalBoot: local.Boot, PeerBoot: remote.Boot}
	records[id] = result
	data, err := json.Marshal(records)
	if err != nil {
		return result, err
	}
	if len(data) > 256<<10 {
		return result, ErrUnsafe
	}
	temp := filepath.Join(root, ".protection-receipts-"+ID())
	out, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return result, err
	}
	defer os.Remove(temp)
	_, err = out.Write(data)
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return result, err
	}
	if err = os.Rename(temp, path); err != nil {
		return result, err
	}
	directory, err := os.Open(root)
	if err != nil {
		return result, err
	}
	err = directory.Sync()
	directory.Close()
	return result, err
}
