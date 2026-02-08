package email

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
)

// encryptionKey — ключ шифрования 32 байта (AES-256)
// Берётся из переменной окружения ENCRYPTION_KEY
// Если не задан — используется дефолтный (только для разработки!)
var defaultKey = []byte("wialon-billing-default-key-32b!!")

// getEncryptionKey возвращает ключ шифрования
func getEncryptionKey() []byte {
	if key := os.Getenv("ENCRYPTION_KEY"); key != "" {
		// Ключ должен быть 32 байта для AES-256
		keyBytes := []byte(key)
		if len(keyBytes) >= 32 {
			return keyBytes[:32]
		}
		// Дополняем до 32 байт если меньше
		padded := make([]byte, 32)
		copy(padded, keyBytes)
		return padded
	}
	return defaultKey
}

// Encrypt шифрует текст с помощью AES-256-GCM
func Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}

	key := getEncryptionKey()
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := aesGCM.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt расшифровывает текст, зашифрованный AES-256-GCM
func Decrypt(encrypted string) (string, error) {
	if encrypted == "" {
		return "", nil
	}

	key := getEncryptionKey()
	ciphertext, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := aesGCM.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", errors.New("зашифрованный текст слишком короткий")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}
