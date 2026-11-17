package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"
)

type sealedRecord struct {
	Version    int      `json:"vaultRecord"`
	ID         string   `json:"id"`
	Sources    []string `json:"sources"`
	Nonce      []byte   `json:"nonce"`
	Ciphertext []byte   `json:"ciphertext"`
}

func (m *vaultManager) recordCipher(sources []string) (cipher.AEAD, error) {
	if m == nil {
		return nil, ErrVaultLocked
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.metadataKey) != 32 {
		return nil, ErrVaultLocked
	}
	for _, source := range sources {
		known := false
		for _, v := range m.states {
			if v.config.Source == source {
				known = true
			}
			if v.config.Source == source && (!v.unlocked || v.fault || !m.isMounted(v.config)) {
				return nil, ErrVaultLocked
			}
		}
		if !known {
			return nil, ErrUnsafe
		}
	}
	block, e := aes.NewCipher(m.metadataKey)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(block)
}
func recordAAD(id, kind string, sources []string) []byte {
	b, _ := json.Marshal([]any{"fileBeans record v1", id, kind, sources})
	return b
}
func (s *Service) encodeRecord(id, kind string, b []byte) ([]byte, error) {
	if !s.sensitiveRecord(b) {
		return b, nil
	}
	sources := []string{}
	for _, v := range s.vaults.states {
		probe := &Service{vaults: &vaultManager{states: []*vaultState{v}}}
		if probe.sensitiveRecord(b) {
			sources = append(sources, v.config.Source)
		}
	}
	a, e := s.vaults.recordCipher(sources)
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, a.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	return json.Marshal(sealedRecord{1, id, sources, nonce, a.Seal(nil, nonce, b, recordAAD(id, kind, sources))})
}
func (s *Service) decodeRecord(id, kind string, b []byte) ([]byte, error) {
	var sealed sealedRecord
	if e := json.Unmarshal(b, &sealed); e != nil {
		return nil, e
	}
	if sealed.Version == 0 {
		if s.sensitiveRecord(b) {
			return nil, ErrVaultLocked
		}
		return b, nil
	}
	if sealed.Version != 1 || sealed.ID != id || len(sealed.Sources) == 0 {
		return nil, ErrUnsafe
	}
	a, e := s.vaults.recordCipher(sealed.Sources)
	if e != nil {
		return nil, e
	}
	if len(sealed.Nonce) != a.NonceSize() {
		return nil, ErrUnsafe
	}
	b, e = a.Open(nil, sealed.Nonce, sealed.Ciphertext, recordAAD(id, kind, sealed.Sources))
	if e != nil {
		return nil, ErrUnsafe
	}
	return b, nil
}

func (s *Service) migrateVaultRecords() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, "SELECT id,kind,body FROM fb_operations FOR UPDATE")
	if e != nil {
		return e
	}
	type update struct {
		id, kind string
		body     []byte
	}
	updates := []update{}
	for rows.Next() {
		var id, kind string
		var b []byte
		if e = rows.Scan(&id, &kind, &b); e != nil {
			rows.Close()
			return e
		}
		if !s.sensitiveRecord(b) {
			continue
		}
		sealed, er := s.encodeRecord(id, kind, b)
		if errors.Is(er, ErrVaultLocked) {
			continue
		}
		if er != nil {
			rows.Close()
			return er
		}
		updates = append(updates, update{id, kind, sealed})
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, u := range updates {
		if _, e = tx.ExecContext(ctx, "UPDATE fb_operations SET body=? WHERE id=? AND kind=?", u.body, u.id, u.kind); e != nil {
			return e
		}
	}
	return tx.Commit()
}
