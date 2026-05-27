package credentials

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"gorm.io/gorm"
)

type dbStore struct {
	db  *gorm.DB
	gcm cipher.AEAD
}

// NewDBStore returns an OpenBaoStore backed by the git-service Postgres DB.
// key must be exactly 32 bytes (AES-256). Values are encrypted with AES-256-GCM
// before writing and decrypted on read. Generate a key with: openssl rand -base64 32
func NewDBStore(db *gorm.DB, key []byte) (OpenBaoStore, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("credential store: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("credential store: cipher init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("credential store: gcm init: %w", err)
	}
	return &dbStore{db: db, gcm: gcm}, nil
}

// seal encrypts plaintext and returns base64(nonce || ciphertext+tag).
func (s *dbStore) seal(plaintext []byte) (string, error) {
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("encrypt: nonce: %w", err)
	}
	return base64.StdEncoding.EncodeToString(s.gcm.Seal(nonce, nonce, plaintext, nil)), nil
}

// open decrypts a value previously produced by seal.
func (s *dbStore) open(encoded string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decrypt: base64: %w", err)
	}
	ns := s.gcm.NonceSize()
	if len(data) < ns {
		return nil, errors.New("decrypt: ciphertext too short")
	}
	pt, err := s.gcm.Open(nil, data[:ns], data[ns:], nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: gcm: %w", err)
	}
	return pt, nil
}

func (s *dbStore) Get(ctx context.Context, ocOrgID, key string) ([]byte, error) {
	if err := validateOrgID(ocOrgID); err != nil {
		return nil, err
	}
	var row struct{ Value string }
	err := s.db.WithContext(ctx).Table("org_secrets").
		Where("oc_org_id = ? AND key = ?", ocOrgID, key).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSecretNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.open(row.Value)
}

func (s *dbStore) Put(ctx context.Context, ocOrgID, key string, value []byte) error {
	if err := validateOrgID(ocOrgID); err != nil {
		return err
	}
	encrypted, err := s.seal(value)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Exec(`
		INSERT INTO org_secrets (oc_org_id, key, value) VALUES (?, ?, ?)
		ON CONFLICT (oc_org_id, key) DO UPDATE
		  SET value = EXCLUDED.value, updated_at = now()`,
		ocOrgID, key, encrypted).Error
}

func (s *dbStore) Delete(ctx context.Context, ocOrgID, key string) error {
	if err := validateOrgID(ocOrgID); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Exec(
		`DELETE FROM org_secrets WHERE oc_org_id = ? AND key = ?`, ocOrgID, key).Error
}
