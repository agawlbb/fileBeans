package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// Called only for an explicitly selected Pi profile. Existing keys are never
// silently replaced: a damaged pair requires an administrator to re-pair clients.
func EnsureNodeTLS(c *Config) (string, error) {
	if c.Profile != "pi" {
		return "", nil
	}
	if c.TLS.Certificate == "" {
		c.TLS = TLSConfig{Certificate: filepath.Join(c.State, "server.crt"), Key: filepath.Join(c.State, "server.key")}
	}
	cert, e := os.ReadFile(c.TLS.Certificate)
	if errors.Is(e, os.ErrNotExist) {
		if _, er := os.Lstat(c.TLS.Key); !errors.Is(er, os.ErrNotExist) {
			return "", ErrConflict
		}
		key, er := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if er != nil {
			return "", er
		}
		serial, er := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if er != nil {
			return "", er
		}
		now := time.Now()
		template := &x509.Certificate{SerialNumber: serial, DNSNames: []string{c.ServerID}, Subject: pkix.Name{CommonName: c.ServerID}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(5, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
		der, er := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if er != nil {
			return "", er
		}
		secret, er := x509.MarshalPKCS8PrivateKey(key)
		if er != nil {
			return "", er
		}
		defer clear(secret)
		if er = os.MkdirAll(c.State, 0700); er != nil {
			return "", er
		}
		write := func(path string, data []byte) error {
			f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				return e
			}
			_, e = f.Write(data)
			if e == nil {
				e = f.Sync()
			}
			closeErr := f.Close()
			if e != nil {
				return e
			}
			return closeErr
		}
		if er = write(c.TLS.Key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: secret})); er != nil {
			return "", er
		}
		cert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		if er = write(c.TLS.Certificate, cert); er != nil {
			return "", er
		}
	} else if e != nil {
		return "", e
	}
	block, _ := pem.Decode(cert)
	if block == nil {
		return "", ErrUnsafe
	}
	leaf, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		return "", e
	}
	if time.Now().Before(leaf.NotBefore) || time.Now().After(leaf.NotAfter) {
		return "", errors.New("Pi certificate expired; renew and explicitly re-pair clients")
	}
	sum := sha256.Sum256(leaf.Raw)
	return hex.EncodeToString(sum[:]), nil
}
