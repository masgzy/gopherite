package protocol

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
)

// CFB8 implements the AES/CFB8/NoPadding stream cipher Minecraft uses for
// protocol encryption. Go's standard library only ships full-block CFB
// (CFB128), so the eight-bit feedback variant lives here.
//
// A 16-byte shift register is initialised with the IV. For every byte the
// whole register is encrypted and the first byte of the result XORed into
// the plaintext (encrypt mode) or ciphertext (decrypt mode); the ciphertext
// byte is then shifted into the register. This matches javax.crypto's
// "AES/CFB8/NoPadding" transformation.
type CFB8 struct {
	aes     cipher.Block
	reg     [aes.BlockSize]byte // feedback shift register
	out     [aes.BlockSize]byte // scratch for one block encryption
	encrypt bool
}

var (
	// ErrBadKey is returned for keys that are not exactly 128 bits.
	ErrBadKey = errors.New("protocol: CFB8 requires a 128-bit key")
	// ErrBadIV is returned for IVs that are not exactly 16 bytes.
	ErrBadIV = errors.New("protocol: CFB8 requires a 16-byte IV")
)

// NewCFB8Encrypter returns a CFB8 keystream cipher in encrypt mode.
func NewCFB8Encrypter(key, iv []byte) (*CFB8, error) {
	return newCFB8(key, iv, true)
}

// NewCFB8Decrypter returns a CFB8 keystream cipher in decrypt mode.
func NewCFB8Decrypter(key, iv []byte) (*CFB8, error) {
	return newCFB8(key, iv, false)
}

func newCFB8(key, iv []byte, encrypt bool) (*CFB8, error) {
	if len(key) != 16 {
		return nil, ErrBadKey
	}
	if len(iv) != aes.BlockSize {
		return nil, ErrBadIV
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	c := &CFB8{aes: block, encrypt: encrypt}
	copy(c.reg[:], iv)
	return c, nil
}

// Crypt processes src and appends the transformed bytes to dst. Encrypting
// and decrypting with the same key/IV but opposite modes are exact inverses.
// Each CFB8 instance must only be used in one direction of one connection.
func (c *CFB8) Crypt(dst, src []byte) []byte {
	for _, b := range src {
		c.aes.Encrypt(c.out[:], c.reg[:])
		ks := c.out[0]
		out := b ^ ks
		copy(c.reg[:15], c.reg[1:])
		if c.encrypt {
			c.reg[15] = out // feed back ciphertext
		} else {
			c.reg[15] = b // feed back ciphertext (the incoming byte)
		}
		dst = append(dst, out)
	}
	return dst
}
