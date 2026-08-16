package pki

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
)

// Store 是 pki 需要的存储能力。
type Store interface {
	GetSetting(ctx context.Context, key string) ([]byte, error)
	PutSetting(ctx context.Context, key string, val []byte) error
}

// ErrNoSecretKey 表示没有提供用于加密 CA 私钥的主密钥。
var ErrNoSecretKey = errors.New("缺少主密钥")

type persisted struct {
	CertPEM []byte `json:"cert"`
	KeyPEM  []byte `json:"key"`
}

// LoadOrCreate 从存储里取出一套 CA；不存在时新建并保存。
//
// CA 私钥**加密后**落库。这是整个系统里最敏感的一份材料：拿到它就能为任意节点
// 签发凭据，而节点凭据能打开隧道、能冒充边缘节点去连源站。没有主密钥时直接
// 拒绝启动，不做「先明文存着以后再加密」——那个「以后」不会到来，而明文的
// CA 私钥躺在库里是不会有任何东西提醒你的。
func LoadOrCreate(ctx context.Context, st Store, key string, secret []byte, name string) (*CA, error) {
	if len(secret) == 0 {
		return nil, fmt.Errorf("%w：无法安全保存 %s 的私钥", ErrNoSecretKey, name)
	}
	blob, err := st.GetSetting(ctx, key)
	if err == nil && len(blob) > 0 {
		return decodeCA(blob, secret, name)
	}

	ca, err := NewCA(name)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(ca.key)
	if err != nil {
		return nil, fmt.Errorf("序列化 %s 私钥: %w", name, err)
	}
	raw, err := json.Marshal(persisted{
		CertPEM: ca.RootPEM(),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	})
	if err != nil {
		return nil, err
	}
	sealed, err := seal(raw, secret)
	if err != nil {
		return nil, fmt.Errorf("加密 %s 私钥: %w", name, err)
	}
	if err := st.PutSetting(ctx, key, sealed); err != nil {
		return nil, err
	}
	return ca, nil
}

func decodeCA(blob, secret []byte, name string) (*CA, error) {
	raw, err := open(blob, secret)
	if err != nil {
		// 主密钥不对时必须报错，不能悄悄新建一套 CA——那会让所有已接入的节点
		// 在下次重连时集体失效，而现象是「节点莫名其妙全掉了」。
		return nil, fmt.Errorf("解密 %s 私钥失败（主密钥是否变了？）: %w", name, err)
	}
	var p persisted
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", name, err)
	}
	certBlock, _ := pem.Decode(p.CertPEM)
	keyBlock, _ := pem.Decode(p.KeyPEM)
	if certBlock == nil || keyBlock == nil {
		return nil, fmt.Errorf("%s 的存档不是合法 PEM", name)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析 %s 证书: %w", name, err)
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析 %s 私钥: %w", name, err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &CA{name: name, cert: cert, key: key, pool: pool}, nil
}

// 设置表里的键。两套 CA 分开存，换其中一套不影响另一套（docs/adr/0009）。
const (
	KeyTunnelCA   = "pki.tunnel_ca"
	KeyUpstreamCA = "pki.upstream_ca"
)

func aead(secret []byte) (cipher.AEAD, error) {
	// 主密钥可以是任意长度的口令，先归一成 32 字节
	sum := sha256.Sum256(secret)
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func seal(plain, secret []byte) ([]byte, error) {
	gcm, err := aead(secret)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, nil), nil
}

func open(sealed, secret []byte) ([]byte, error) {
	gcm, err := aead(secret)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errors.New("密文过短")
	}
	return gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
}
