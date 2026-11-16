package main

// Binary patches ("YHP1"). A patch turns one exact file (identified by its
// SHA-256) into another, and contains only the bytes that differ, so a mod for
// a packed game file can be shipped without shipping the game file itself.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

const patchMagic = "YHP1"

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// makePatch builds a patch that turns src into dst.
func makePatch(src, dst []byte) []byte {
	const B = 32
	const prime = 1099511628211
	var pow uint64 = 1
	for i := 0; i < B-1; i++ {
		pow *= prime
	}
	hashAt := func(b []byte) uint64 {
		var h uint64
		for _, c := range b[:B] {
			h = h*prime + uint64(c)
		}
		return h
	}
	index := make(map[uint64]int32, len(src)/B+1)
	for off := 0; off+B <= len(src); off += B {
		h := hashAt(src[off:])
		if _, ok := index[h]; !ok {
			index[h] = int32(off)
		}
	}

	var raw bytes.Buffer
	tmp := make([]byte, binary.MaxVarintLen64)
	putU := func(v uint64) { raw.Write(tmp[:binary.PutUvarint(tmp, v)]) }
	lit := 0 // start of pending literal run in dst
	flush := func(end int) {
		if end > lit {
			raw.WriteByte(1)
			putU(uint64(end - lit))
			raw.Write(dst[lit:end])
		}
	}

	i := 0
	var h uint64
	have := false
	for i+B <= len(dst) {
		if !have {
			h = hashAt(dst[i:])
			have = true
		}
		if so, ok := index[h]; ok && bytes.Equal(src[so:int(so)+B], dst[i:i+B]) {
			s, d := int(so), i
			for s > 0 && d > lit && src[s-1] == dst[d-1] {
				s--
				d--
			}
			e := 0
			for int(so)+B+e < len(src) && i+B+e < len(dst) && src[int(so)+B+e] == dst[i+B+e] {
				e++
			}
			length := (i - d) + B + e
			flush(d)
			raw.WriteByte(0)
			putU(uint64(s))
			putU(uint64(length))
			i = d + length
			lit = i
			have = false
			continue
		}
		if i+B < len(dst) {
			h = (h-uint64(dst[i])*pow)*prime + uint64(dst[i+B])
		}
		i++
	}
	flush(len(dst))

	var out bytes.Buffer
	out.WriteString(patchMagic)
	fs, ds := sha256.Sum256(src), sha256.Sum256(dst)
	out.Write(fs[:])
	out.Write(ds[:])
	binary.Write(&out, binary.LittleEndian, uint64(len(dst)))
	zw, _ := gzip.NewWriterLevel(&out, gzip.BestCompression)
	zw.Write(raw.Bytes())
	zw.Close()
	return out.Bytes()
}

// applyPatch returns the patched file. It refuses a source that is not the
// exact file the patch was made from and verifies the result.
func applyPatch(src, patch []byte) ([]byte, error) {
	if len(patch) < 4+32+32+8 || string(patch[:4]) != patchMagic {
		return nil, errors.New("not a yutakhub patch file")
	}
	from, to := patch[4:36], patch[36:68]
	size := binary.LittleEndian.Uint64(patch[68:76])
	if size > 4<<30 {
		return nil, errors.New("patch output too large")
	}
	if s := sha256.Sum256(src); !bytes.Equal(s[:], from) {
		return nil, errors.New("patch does not match this file")
	}
	zr, err := gzip.NewReader(bytes.NewReader(patch[76:]))
	if err != nil {
		return nil, err
	}
	r := bufio.NewReader(zr)
	out := make([]byte, 0, size)
	for {
		op, err := r.ReadByte()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch op {
		case 0:
			off, e1 := binary.ReadUvarint(r)
			n, e2 := binary.ReadUvarint(r)
			if e1 != nil || e2 != nil || off > uint64(len(src)) || n > uint64(len(src))-off {
				return nil, errors.New("corrupt patch")
			}
			out = append(out, src[off:off+n]...)
		case 1:
			n, e1 := binary.ReadUvarint(r)
			if e1 != nil || n > size {
				return nil, errors.New("corrupt patch")
			}
			start := len(out)
			out = append(out, make([]byte, n)...)
			if _, err := io.ReadFull(r, out[start:]); err != nil {
				return nil, errors.New("corrupt patch")
			}
		default:
			return nil, errors.New("corrupt patch")
		}
		if uint64(len(out)) > size {
			return nil, errors.New("corrupt patch")
		}
	}
	if s := sha256.Sum256(out); uint64(len(out)) != size || !bytes.Equal(s[:], to) {
		return nil, errors.New("patched file failed verification")
	}
	return out, nil
}

func cmdMkpatch(args []string) error {
	if len(args) != 3 {
		return errors.New("usage: yutakhub mkpatch <original> <modded> <out.yhp>")
	}
	src, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	dst, err := os.ReadFile(args[1])
	if err != nil {
		return err
	}
	p := makePatch(src, dst)
	if _, err := applyPatch(src, p); err != nil {
		return fmt.Errorf("self-check failed: %w", err)
	}
	if err := os.WriteFile(args[2], p, 0o644); err != nil {
		return err
	}
	fmt.Printf("from %s\nto   %s\npatch %d bytes\n", sha256Hex(src), sha256Hex(dst), len(p))
	return nil
}
