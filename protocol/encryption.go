package protocol

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/binary"
	"math/big"
)

// KeyPair holds the server's RSA-1024 login keypair (vanilla uses 1024-bit
// keys; sessions are short-lived and the key only protects the shared
// secret exchange, so this stays compatible with vanilla clients).
type KeyPair struct {
	Private *rsa.PrivateKey
	Public  []byte // X.509 PKIX DER encoding, as sent in Encryption Request
}

// GenerateKeyPair creates a fresh 1024-bit RSA login keypair.
func GenerateKeyPair() (*KeyPair, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		return nil, err
	}
	pub, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, err
	}
	return &KeyPair{Private: priv, Public: pub}, nil
}

// EncryptPKCS1v1 encrypts msg with the server's public key, as used by the
// client for the shared secret and verify token.
func (k *KeyPair) EncryptPKCS1v1(msg []byte) ([]byte, error) {
	return rsa.EncryptPKCS1v15(rand.Reader, &k.Private.PublicKey, msg)
}

// DecryptPKCS1v1 decrypts a client-encrypted blob with the private key.
func (k *KeyPair) DecryptPKCS1v1(msg []byte) ([]byte, error) {
	return rsa.DecryptPKCS1v15(rand.Reader, k.Private, msg)
}

// AuthDigest computes the Mojang session server digest: SHA-1 over the
// server id (empty string for vanilla), the shared secret and the DER
// public key. Vanilla renders it as new BigInteger(1, sum).toString(16):
// always positive, lowercase hex, no zero padding.
func AuthDigest(serverID string, sharedSecret, publicKey []byte) string {
	h := sha1.New()
	h.Write([]byte(serverID))
	h.Write(sharedSecret)
	h.Write(publicKey)
	return new(big.Int).SetBytes(h.Sum(nil)).Text(16)
}

// OfflinePlayerUUID mirrors UUIDUtil.createOfflinePlayerUUID: a v3 (MD5)
// UUID computed over "OfflinePlayer:" + name with no namespace.
func OfflinePlayerUUID(name string) [16]byte {
	data := append([]byte("OfflinePlayer:"), name...)
	sum := md5Sum(data)
	sum[6] = (sum[6] & 0x0F) | 0x30 // version 3
	sum[8] = (sum[8] & 0x3F) | 0x80 // IETF variant
	var out [16]byte
	copy(out[:], sum)
	return out
}

// RandomChallenge produces a four-byte verify token as vanilla does
// (Ints.toByteArray(RandomSource.nextInt())).
func RandomChallenge() ([]byte, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	return b[:], nil
}

// RandomLong produces a random int64 for keep-alive and session ids.
func RandomLong() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(b[:])), nil
}

func md5Sum(data []byte) []byte {
	sum := md5.Sum(data)
	out := make([]byte, len(sum))
	copy(out, sum[:])
	return out
}
