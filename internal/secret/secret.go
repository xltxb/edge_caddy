// Package secret 是落库前的对称加密。
//
// 只有一份实现，供 CA 私钥与告警渠道凭据共用。出现第二份实现意味着迟早有一边
// 用错模式或漏掉 nonce，而密文躺在库里是不会有任何东西提醒你的。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
)

// ErrNoKey 表示没有提供主密钥。
var ErrNoKey = errors.New("缺少主密钥")

func aead(key []byte) (cipher.AEAD, error) {
	if len(key) == 0 {
		return nil, ErrNoKey
	}
	// 主密钥可以是任意长度的口令，先归一成 32 字节
	sum := sha256.Sum256(key)
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal 加密。nonce 随机生成并前置在密文里。
func Seal(plain, key []byte) ([]byte, error) {
	gcm, err := aead(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, nil), nil
}

// Open 解密。主密钥不对时报错，绝不返回半截明文。
func Open(sealed, key []byte) ([]byte, error) {
	gcm, err := aead(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errors.New("密文过短")
	}
	return gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
}
