package protocol

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// Vector generated with OpenJDK 21 javax.crypto AES/CFB8/NoPadding by
// scripts/GenCfb8Vector.java: the exact transformation vanilla clients use
// for the protocol cipher. Guards the hand-rolled CFB8 against drift.
var cfb8Vector = struct {
	key, iv, plain, cipher string
}{
	key:    "101112131415161718191a1b1c1d1e1f",
	iv:     "f0efeeedecebeae9e8e7e6e5e4e3e2e1",
	plain:  "030a11181f262d343b424950575e656c737a81888f969da4abb2b9c0c7ced5dce3eaf1f8ff060d141b222930373e454c535a61686f767d848b9299a0a7aeb5bcc3cad1d8dfe6edf4fb020910171e252c333a41",
	cipher: "7a1241d588df0f773a2b7f35bf953232541d3e1a2f0c563d548d7040343807b4eb1eeb46ff92ad5736ec84822913ef0793e5bbe82a45de6bcfe6dc0b0f3772bf42b7d8b48d0c7e146ddf2f837ffaf85acbff63",
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex vector: %v", err)
	}
	return b
}

func TestCFB8KnownAnswer(t *testing.T) {
	key := mustHex(t, cfb8Vector.key)
	iv := mustHex(t, cfb8Vector.iv)
	plain := mustHex(t, cfb8Vector.plain)
	want := mustHex(t, cfb8Vector.cipher)

	enc, err := NewCFB8Encrypter(key, iv)
	if err != nil {
		t.Fatalf("encrypter: %v", err)
	}
	got := enc.Crypt(nil, plain)
	if !bytes.Equal(got, want) {
		t.Fatalf("cipher mismatch:\n got  %x\n want %x", got, want)
	}

	dec, err := NewCFB8Decrypter(key, iv)
	if err != nil {
		t.Fatalf("decrypter: %v", err)
	}
	back := dec.Crypt(nil, got)
	if !bytes.Equal(back, plain) {
		t.Fatalf("roundtrip mismatch:\n got  %x\n want %x", back, plain)
	}
}

func TestCFB8IncrementalEqualsWhole(t *testing.T) {
	key := mustHex(t, cfb8Vector.key)
	iv := mustHex(t, cfb8Vector.iv)
	plain := mustHex(t, cfb8Vector.plain)
	enc, _ := NewCFB8Encrypter(key, iv)
	whole := enc.Crypt(nil, plain)

	enc2, _ := NewCFB8Encrypter(key, iv)
	var split []byte
	split = enc2.Crypt(split, plain[:7])
	split = enc2.Crypt(split, plain[7:40])
	split = enc2.Crypt(split, plain[40:])
	if !bytes.Equal(split, whole) {
		t.Fatalf("streaming crypt diverged from single-shot")
	}
}

func TestCFB8BadKeyIV(t *testing.T) {
	key := make([]byte, 16)
	iv := make([]byte, 16)
	if _, err := NewCFB8Encrypter(make([]byte, 15), iv); err != ErrBadKey {
		t.Fatalf("want ErrBadKey, got %v", err)
	}
	if _, err := NewCFB8Encrypter(key, make([]byte, 8)); err != ErrBadIV {
		t.Fatalf("want ErrBadIV, got %v", err)
	}
}
