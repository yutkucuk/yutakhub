package main

// Locked packages. A game that is not meant for everyone is published encrypted: the file in the
// repository is useless without the access code its owner hands out, and the app asks for that code
// before it installs or updates the game. What is kept on GitHub is then only ciphertext.
//
// The file:  "YHX1" | iterations (4 bytes, big endian) | salt (16) | nonce (12) | AES-256-GCM(zip)
// The key:   PBKDF2-HMAC-SHA256(code, salt, iterations), 32 bytes. The header is authenticated with the data.
// The code:  letters and digits; case, spaces and dashes do not matter when it is typed.
//
// The catalog carries the salt, the iteration count and a short check value made from the key, so a
// mistyped code is turned away at once, before anything is downloaded.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
)

const (
	lockMagic   = "YHX1"
	lockIter    = 200000
	lockHeadLen = 4 + 4 + 16
	// no 0/O and 1/I: a code is read out and typed by people
	codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

type Lock struct {
	Salt  string `json:"salt"`
	Iter  int    `json:"iter"`
	Check string `json:"check"`
}

var errBadKey = errors.New("wrong access code")

// normCode is the code as it is used: upper case, letters and digits only.
func normCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// newCode makes a code of four groups of five: 100 bits, far beyond guessing.
func newCode() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	var b strings.Builder
	for i, v := range raw {
		if i > 0 && i%5 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(codeAlphabet[int(v)%len(codeAlphabet)])
	}
	return b.String(), nil
}

func pbkdf2SHA256(pw, salt []byte, iter, n int) []byte {
	mac := hmac.New(sha256.New, pw)
	var out []byte
	for block := uint32(1); len(out) < n; block++ {
		mac.Reset()
		mac.Write(salt)
		var idx [4]byte
		binary.BigEndian.PutUint32(idx[:], block)
		mac.Write(idx[:])
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iter; i++ {
			mac.Reset()
			mac.Write(u)
			u = mac.Sum(u[:0])
			for k := range t {
				t[k] ^= u[k]
			}
		}
		out = append(out, t...)
	}
	return out[:n]
}

func lockKey(code string, salt []byte, iter int) []byte {
	return pbkdf2SHA256([]byte(normCode(code)), salt, iter, 32)
}

func lockCheck(key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("yutakhub access check"))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

// matches says whether code is the one this package was locked with.
func (l *Lock) matches(code string) bool {
	salt, err := hex.DecodeString(l.Salt)
	if err != nil || len(salt) == 0 || l.Iter < 1 || l.Iter > 5000000 || normCode(code) == "" {
		return false
	}
	return hmac.Equal([]byte(lockCheck(lockKey(code, salt, l.Iter))), []byte(strings.ToLower(l.Check)))
}

func isLocked(data []byte) bool {
	return len(data) > lockHeadLen+12 && string(data[:4]) == lockMagic
}

// lockSeal encrypts a package and returns what the catalog has to say about it.
func lockSeal(plain []byte, code string) ([]byte, *Lock, error) {
	if len(normCode(code)) < 12 {
		return nil, nil, errors.New("the access code is too short")
	}
	head := make([]byte, lockHeadLen, lockHeadLen+12+len(plain)+16)
	copy(head, lockMagic)
	binary.BigEndian.PutUint32(head[4:8], lockIter)
	if _, err := rand.Read(head[8:]); err != nil {
		return nil, nil, err
	}
	key := lockKey(code, head[8:], lockIter)
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	out := append(head, nonce...)
	out = gcm.Seal(out, nonce, plain, head)
	return out, &Lock{Salt: hex.EncodeToString(head[8:]), Iter: lockIter, Check: lockCheck(key)}, nil
}

// lockOpen gives back the package, or errBadKey when the code is not the right one
// (a file that was tampered with fails the same way: nothing of it is used).
func lockOpen(data []byte, code string) ([]byte, error) {
	if !isLocked(data) {
		return nil, errors.New("not a locked package")
	}
	head := data[:lockHeadLen]
	iter := int(binary.BigEndian.Uint32(head[4:8]))
	if iter < 1 || iter > 5000000 {
		return nil, errors.New("not a locked package")
	}
	blk, err := aes.NewCipher(lockKey(code, head[8:], iter))
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	rest := data[lockHeadLen:]
	if len(rest) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("not a locked package")
	}
	plain, err := gcm.Open(nil, rest[:gcm.NonceSize()], rest[gcm.NonceSize():], head)
	if err != nil {
		return nil, errBadKey
	}
	if !bytes.HasPrefix(plain, []byte("PK")) {
		return nil, errors.New("the locked package does not hold a zip")
	}
	return plain, nil
}
