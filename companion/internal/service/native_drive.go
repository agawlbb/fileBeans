package service

// An explicitly authorized Drive transfer uses a job-scoped native OAuth grant.
// The client encrypts it to this server; it never enters a public job record or
// replaces the legacy server account. Completed/cancelled grants are removed.
import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type SealedDriveGrant struct {
	Ephemeral []byte `json:"ephemeral"`
	Sealed    []byte `json:"sealed"`
}
type nativeDriveCredential struct {
	Deadline int64            `json:"deadline"`
	Account  string           `json:"account"`
	Grant    SealedDriveGrant `json:"grant"`
}

func (s *Service) transferPrivateKey() (*ecdh.PrivateKey, error) {
	path := filepath.Join(s.config.State, "transfer-x25519.key")
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(e, os.ErrNotExist) {
		key, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if err == nil {
			f := os.NewFile(uintptr(fd), path)
			_, err = f.Write(key.Bytes())
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
			if err != nil {
				return nil, err
			}
			return key, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		fd, e = unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		return nil, ErrUnsafe
	}
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Uid != uint32(os.Geteuid()) {
		return nil, ErrUnsafe
	}
	raw, e := io.ReadAll(io.LimitReader(f, 33))
	if e != nil || len(raw) != 32 {
		return nil, ErrUnsafe
	}
	defer clear(raw)
	return ecdh.X25519().NewPrivateKey(raw)
}
func (s *Service) transferPublicKey() (string, error) {
	key, e := s.transferPrivateKey()
	if e != nil {
		return "", e
	}
	return base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}
func nativeGrantAEAD(shared, ephemeral, server []byte, serverID string) (cipher.AEAD, []byte, error) {
	info := append([]byte("fileBeans.native-drive.v1:"+serverID), ephemeral...)
	info = append(info, server...)
	// HKDF-SHA256, empty salt, one 32-byte output block (RFC 5869).
	extract := hmac.New(sha256.New, nil)
	extract.Write(shared)
	prk := extract.Sum(nil)
	defer clear(prk)
	expand := hmac.New(sha256.New, prk)
	expand.Write(info)
	expand.Write([]byte{1})
	key := expand.Sum(nil)
	defer clear(key)
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, nil, e
	}
	aead, e := cipher.NewGCM(block)
	return aead, info, e
}
func (s *Service) openNativeDriveGrant(envelope SealedDriveGrant) (Google, error) {
	var grant Google
	if len(envelope.Ephemeral) != 32 || len(envelope.Sealed) < 28 || len(envelope.Sealed) > 16384 {
		return grant, ErrUnsafe
	}
	key, e := s.transferPrivateKey()
	if e != nil {
		return grant, e
	}
	peer, e := ecdh.X25519().NewPublicKey(envelope.Ephemeral)
	if e != nil {
		return grant, ErrUnsafe
	}
	shared, e := key.ECDH(peer)
	if e != nil {
		return grant, ErrUnsafe
	}
	defer clear(shared)
	aead, info, e := nativeGrantAEAD(shared, envelope.Ephemeral, key.PublicKey().Bytes(), s.config.ServerID)
	if e != nil {
		return grant, e
	}
	raw, e := aead.Open(nil, envelope.Sealed[:aead.NonceSize()], envelope.Sealed[aead.NonceSize():], info)
	if e != nil {
		return grant, ErrAuthorization
	}
	defer clear(raw)
	if json.Unmarshal(raw, &grant) != nil || grant.Access == "" || grant.Refresh == "" || len(grant.Access) > 8192 || len(grant.Refresh) > 8192 || !strings.HasSuffix(grant.ClientID, ".apps.googleusercontent.com") || grant.Account == "" || len(grant.Account) > 128 || !grant.writable() {
		return Google{}, ErrAuthorization
	}
	return grant, nil
}
func (s *Service) sealNativeDriveGrant(grant Google) (SealedDriveGrant, error) {
	var out SealedDriveGrant
	key, e := s.transferPrivateKey()
	if e != nil {
		return out, e
	}
	ephemeral, e := ecdh.X25519().GenerateKey(rand.Reader)
	if e != nil {
		return out, e
	}
	shared, e := ephemeral.ECDH(key.PublicKey())
	if e != nil {
		return out, e
	}
	defer clear(shared)
	out.Ephemeral = ephemeral.PublicKey().Bytes()
	aead, info, e := nativeGrantAEAD(shared, out.Ephemeral, key.PublicKey().Bytes(), s.config.ServerID)
	if e != nil {
		return out, e
	}
	nonce := make([]byte, aead.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return out, e
	}
	raw, e := json.Marshal(grant)
	if e != nil {
		return out, e
	}
	defer clear(raw)
	out.Sealed = append(nonce, aead.Seal(nil, nonce, raw, info)...)
	return out, nil
}
func (s *Service) nativeCredentialDirectory() (string, error) {
	path := filepath.Join(s.config.State, "native-drive-grants")
	if e := os.Mkdir(path, 0700); e != nil && !errors.Is(e, os.ErrExist) {
		return "", e
	}
	st, e := os.Lstat(path)
	if e != nil || !st.IsDir() || st.Mode().Perm() != 0700 {
		return "", ErrUnsafe
	}
	return path, nil
}
func nativeCredentialID(connection string) (string, error) {
	id := strings.TrimPrefix(connection, "native:")
	if !strings.HasPrefix(connection, "native:") || len(id) != 32 {
		return "", ErrUnsafe
	}
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return "", ErrUnsafe
		}
	}
	return id, nil
}
func (s *Service) saveNativeGoogle(connection string, grant Google, deadline int64) error {
	id, e := nativeCredentialID(connection)
	if e != nil {
		return e
	}
	dir, e := s.nativeCredentialDirectory()
	if e != nil {
		return e
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return e
	}
	if len(entries) >= 128 {
		if _, e = os.Lstat(filepath.Join(dir, id+".json")); e != nil {
			return ErrVaultBusy
		}
	}
	sealed, e := s.sealNativeDriveGrant(grant)
	if e != nil {
		return e
	}
	return privateJSON(filepath.Join(dir, id+".json"), nativeDriveCredential{deadline, grant.Account, sealed})
}
func (s *Service) nativeGoogle(connection string) (Google, int64, error) {
	id, e := nativeCredentialID(connection)
	if e != nil {
		return Google{}, 0, e
	}
	dir, e := s.nativeCredentialDirectory()
	if e != nil {
		return Google{}, 0, e
	}
	fd, e := unix.Open(filepath.Join(dir, id+".json"), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return Google{}, 0, ErrAuthorization
	}
	f := os.NewFile(uintptr(fd), id)
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, 32769))
	if e != nil || len(raw) > 32768 {
		return Google{}, 0, ErrUnsafe
	}
	var credential nativeDriveCredential
	if json.Unmarshal(raw, &credential) != nil || credential.Deadline < time.Now().Unix() {
		return Google{}, 0, ErrAuthorization
	}
	grant, e := s.openNativeDriveGrant(credential.Grant)
	if e != nil {
		return Google{}, 0, e
	}
	if credential.Account != grant.Account {
		return Google{}, 0, ErrUnsafe
	}
	grant.Connection = connection
	return grant, credential.Deadline, nil
}
func (s *Service) googleForConnection(connection string) (Google, error) {
	if strings.HasPrefix(connection, "native:") {
		g, _, e := s.nativeGoogle(connection)
		return g, e
	}
	g, e := s.google()
	if e == nil && connection != "" && g.Connection != connection {
		return g, ErrDriveAccount
	}
	return g, e
}
func (s *Service) removeNativeGoogle(connection string) {
	id, e := nativeCredentialID(connection)
	if e != nil {
		return
	}
	dir, e := s.nativeCredentialDirectory()
	if e == nil {
		_ = os.Remove(filepath.Join(dir, id+".json"))
	}
}

func (s *Service) cleanupExpiredNativeGoogle() {
	dir, e := s.nativeCredentialDirectory()
	if e != nil {
		return
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) > 256 {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if _, e = nativeCredentialID("native:" + id); e != nil {
			continue
		}
		fd, err := unix.Open(filepath.Join(dir, entry.Name()), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		f := os.NewFile(uintptr(fd), entry.Name())
		raw, err := io.ReadAll(io.LimitReader(f, 32769))
		f.Close()
		var credential nativeDriveCredential
		if err == nil && len(raw) <= 32768 && json.Unmarshal(raw, &credential) == nil && credential.Deadline < time.Now().Unix() {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
func (s *Service) renewNativeGoogle(r *http.Request, j *Job) error {
	if !strings.HasPrefix(j.Connection, "native:") {
		return nil
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, 32769))
	if e != nil || len(raw) > 32768 {
		return ErrUnsafe
	}
	if len(raw) == 0 {
		return nil
	}
	var input struct {
		Grant   *SealedDriveGrant `json:"driveGrant"`
		Account string            `json:"driveAccount"`
	}
	if json.Unmarshal(raw, &input) != nil || input.Grant == nil || input.Account != j.Request.DriveAccount {
		return ErrUnsafe
	}
	grant, e := s.openNativeDriveGrant(*input.Grant)
	if e != nil || grant.Account != input.Account {
		return ErrAuthorization
	}
	return s.saveNativeGoogle(j.Connection, grant, time.Now().Add(24*time.Hour).Unix())
}
