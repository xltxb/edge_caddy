package secret

import (
	"bytes"
	"errors"
	"testing"
)

const key = "a-master-key-that-is-long-enough!!"

func TestSealOpenRoundTrip(t *testing.T) {
	s, err := New([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("-----BEGIN PRIVATE KEY-----")
	sealed, err := s.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, plain) {
		t.Fatal("密文里出现了明文")
	}
	got, err := s.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("往返后 = %q", got)
	}
}

// 同一段明文两次密封必须不同。相同会说明 nonce 被复用了，
// 而 GCM 下 nonce 复用是彻底失效，不是「稍微弱一点」。
func TestSealIsNonDeterministic(t *testing.T) {
	s, _ := New([]byte(key))
	a, _ := s.Seal([]byte("same"))
	b, _ := s.Seal([]byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("两次密封结果相同——nonce 被复用了")
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	s, _ := New([]byte(key))
	sealed, _ := s.Seal([]byte("payload"))
	sealed[len(sealed)-1] ^= 0xff
	if _, err := s.Open(sealed); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("被篡改的密文应当解不开，err=%v", err)
	}
}

func TestOpenRejectsWrongKey(t *testing.T) {
	a, _ := New([]byte(key))
	b, _ := New([]byte("a-completely-different-master-key!"))
	sealed, _ := a.Seal([]byte("payload"))
	if _, err := b.Open(sealed); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("换一把密钥应当解不开，err=%v", err)
	}
}

func TestShortKeyRejected(t *testing.T) {
	if _, err := New([]byte("too-short")); err == nil {
		t.Fatal("过短的主密钥应当被拒绝")
	}
}
