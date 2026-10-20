package cloud

// Obscure and Reveal reproduce rclone's lib/obscure, which rclone uses for
// passwords in its configuration (a crypt remote's password and salt among
// them). The algorithm and the fixed key below are copied from rclone,
// https://github.com/rclone/rclone/blob/master/lib/obscure/obscure.go,
// Copyright (C) 2012 by Nick Craig-Wood http://www.craig-wood.com/nick/,
// under the MIT licence.
//
// Obscuring is not encryption: the key is public, and anyone holding an
// obscured value can reveal it. It only keeps a password from being read
// at a glance, and it is what rclone expects in RCLONE_CONFIG_*_PASSWORD.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// obscureKey is rclone's fixed, public AES-256 key for obscuring.
var obscureKey = []byte{
	0x9c, 0x93, 0x5b, 0x48, 0x73, 0x0a, 0x55, 0x4d,
	0x6b, 0xfd, 0x7c, 0x63, 0xc8, 0x86, 0xa9, 0x2b,
	0xd3, 0x90, 0x19, 0x8e, 0xb8, 0x12, 0x8a, 0xfb,
	0xf4, 0xde, 0x16, 0x2b, 0x8b, 0x95, 0xf6, 0x38,
}

// obscureCTR XORs in into out with AES-CTR under obscureKey and iv.
func obscureCTR(out, in, iv []byte) error {
	block, err := aes.NewCipher(obscureKey)
	if err != nil {
		return err
	}
	cipher.NewCTR(block, iv).XORKeyStream(out, in)
	return nil
}

// Obscure returns x obscured as `rclone obscure` does: a random IV followed
// by the AES-CTR ciphertext, base64url without padding.
func Obscure(x string) (string, error) {
	return obscureWith(rand.Reader, x)
}

func obscureWith(random io.Reader, x string) (string, error) {
	plaintext := []byte(x)
	ciphertext := make([]byte, aes.BlockSize+len(plaintext))
	iv := ciphertext[:aes.BlockSize]
	if _, err := io.ReadFull(random, iv); err != nil {
		return "", fmt.Errorf("cloud: obscure: reading IV: %w", err)
	}
	if err := obscureCTR(ciphertext[aes.BlockSize:], plaintext, iv); err != nil {
		return "", fmt.Errorf("cloud: obscure: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

// Reveal undoes Obscure (and `rclone obscure`).
func Reveal(x string) (string, error) {
	ciphertext, err := base64.RawURLEncoding.DecodeString(x)
	if err != nil {
		return "", fmt.Errorf("cloud: reveal: base64 decode failed when revealing password - is it obscured?: %w", err)
	}
	if len(ciphertext) < aes.BlockSize {
		return "", errors.New("cloud: reveal: input too short when revealing password - is it obscured?")
	}
	buf := ciphertext[aes.BlockSize:]
	iv := ciphertext[:aes.BlockSize]
	if err := obscureCTR(buf, buf, iv); err != nil {
		return "", fmt.Errorf("cloud: reveal: %w", err)
	}
	return string(buf), nil
}
