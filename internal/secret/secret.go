// Package secret 封装静态凭据的加密。
//
// DNS 服务商凭据、Lark webhook、两套 CA 的根私钥都以密文落库，
// 任何接口不回显明文（PRD §7）。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

var ErrDecrypt = errors.New("解密失败：密钥不对，或密文已损坏")

// Sealer 用一把主密钥做 AES-256-GCM。
//
// 主密钥来自 EC_SECRET_KEY，长度不定，因此先过一次 SHA-256 归一到 32 字节——
// 直接把变长的环境变量当密钥用会在长度不是 16/24/32 时静默取不到 AES-256。
type Sealer struct{ aead cipher.AEAD }

func New(masterKey []byte) (*Sealer, error) {
	if len(masterKey) < 32 {
		return nil, fmt.Errorf("主密钥太短（%d 字节），至少 32", len(masterKey))
	}
	sum := sha256.Sum256(masterKey)
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal 返回 nonce||ciphertext。nonce 每次随机——GCM 下重复使用同一个 nonce
// 会同时毁掉机密性与完整性，这不是「稍微弱一点」而是彻底失效。
func (s *Sealer) Seal(plain []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("生成 nonce: %w", err)
	}
	return s.aead.Seal(nonce, nonce, plain, nil), nil
}

func (s *Sealer) Open(sealed []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return nil, ErrDecrypt
	}
	plain, err := s.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}
