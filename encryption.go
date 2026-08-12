package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
)

// getMasterKey reads and decodes the master encryption key from the
// environment. This key never touches the database - only it can decrypt
// what's stored there.
func getMasterKey() ([]byte, error) {
	encoded := os.Getenv("COSTRA_MASTER_KEY")
	if encoded == "" {
		return nil, errors.New("COSTRA_MASTER_KEY is not set")
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("COSTRA_MASTER_KEY is not valid base64")
	}
	if len(key) != 32 {
		return nil, errors.New("COSTRA_MASTER_KEY must decode to exactly 32 bytes (AES-256)")
	}
	return key, nil
}

// encrypt turns plaintext (a real provider API key) into a base64 string
// safe to store in the database. AES-GCM requires a random "nonce" (a
// number used once) for every encryption - we generate one, and prepend
// it to the ciphertext so decrypt() can find it again later.
func encrypt(plaintext string) (string, error) {
	key, err := getMasterKey()
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	// Seal encrypts and appends the result to our nonce, so the output is
	// [nonce][ciphertext] combined together - everything decrypt() needs.
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decrypt reverses encrypt() - given the stored base64 string, returns the
// original plaintext API key.
func decrypt(encoded string) (string, error) {
	key, err := getMasterKey()
	if err != nil {
		return "", err
	}

	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("stored key is corrupted or too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
