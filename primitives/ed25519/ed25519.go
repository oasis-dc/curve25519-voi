// Copyright (c) 2016 The Go Authors. All rights reserved.
// Copyright (c) 2019 Oasis Labs Inc.  All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are
// met:
//
//   * Redistributions of source code must retain the above copyright
// notice, this list of conditions and the following disclaimer.
//    * Redistributions in binary form must reproduce the above
// copyright notice, this list of conditions and the following disclaimer
// in the documentation and/or other materials provided with the
// distribution.
//    * Neither the name of Google Inc. nor the names of its
// contributors may be used to endorse or promote products derived from
// this software without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
// OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
// SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
// LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
// DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
// THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

// Package ed25519 implements the Ed25519 signature algorithm. See
// https://ed25519.cr.yp.to/.
//
// These functions are mostly compatible with the “Ed25519” function defined in
// RFC 8032. However, unlike RFC 8032's formulation, this package's private key
// representation includes a public key suffix to make multiple signing
// operations with the same key more efficient. This package refers to the RFC
// 8032 private key as the “seed”.
//
// The default verification behavior is neither identical to that of the
// runtime library, nor that specified in FIPS 186-5.  If exact compability
// is required, use the appropriate VerifyOptions presets.
package ed25519

import (
	"bytes"
	"crypto"
	cryptorand "crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"

	"github.com/oasisprotocol/curve25519-voi/curve"
	"github.com/oasisprotocol/curve25519-voi/curve/scalar"
)

const (
	// PublicKeySize is the size, in bytes, of public keys as used in this package.
	PublicKeySize = 32

	// PrivateKeySize is the size, in bytes, of private keys as used in this package.
	PrivateKeySize = 64

	// SignatureSize is the size, in bytes, of signatures generated and verified by this package.
	SignatureSize = 64

	// SeedSize is the size, in bytes, of private key seeds. These are the private key representations used by RFC 8032.
	SeedSize = 32

	// ContextMaxSize is the maximum allowed context length for Ed25519ctx.
	ContextMaxSize = 255
)

var (
	// VerifyOptionsRuntime specifies verification behavior that is
	// compatible with that provided by the Go `crypto/ed25519` package.
	//
	// Note: This preset is incompatible with batch verification.
	VerifyOptionsRuntime = &VerifyOptions{
		AllowSmallOrderA:    true,
		AllowSmallOrderR:    true,
		AllowNonCanonicalAR: true,
		CofactorlessVerify:  true,
	}

	// VerifyOptionsFIPS_186_5 specifies verification behavior that is
	// compatible with FIPS 186-5.
	VerifyOptionsFIPS_186_5 = &VerifyOptions{
		AllowSmallOrderA: true,
		AllowSmallOrderR: true,
	}

	_ crypto.Signer = (PrivateKey)(nil)

	defaultPureOptions = &Options{
		Verify: &VerifyOptions{},
	}
)

// Options can be used with PrivateKey.Sign or VerifyWithOptions
// to select Ed25519 variants.
type Options struct {
	// Hash can be crypto.Hash(0) for Ed25519/Ed25519ctx, or crypto.SHA512
	// for Ed25519ph.
	Hash crypto.Hash

	// Context is an optional domain separation context for Ed25519ph and
	// Ed25519ctx. It must be less than or equal to ContextMaxSize
	// in length.
	//
	// Warning: If Hash is crypto.Hash(0) and Context is a zero length
	// string, plain Ed25519 will be used instead of Ed25519ctx.
	Context string

	// Verify allows specifying verification behavior for compatibility
	// with other Ed25519 implementations.  If left unspecified, the
	// default value will be used, which should be acceptable for most
	// use cases.
	Verify *VerifyOptions
}

// HashFunc returns an identifier for the hash function used to produce
// the message pased to Signer.Sign. For the Ed25519 family this must
// be crypto.Hash(0) for Ed25519/Ed25519ctx, or crypto.SHA512 for
// Ed25519ph.
func (opt *Options) HashFunc() crypto.Hash {
	return opt.Hash
}

func (opt *Options) verify() (dom2Flag, []byte, error) {
	var (
		context []byte
		f       dom2Flag = fPure
	)

	if l := len(opt.Context); l > 0 {
		if l > ContextMaxSize {
			return f, nil, fmt.Errorf("ed25519: bad context length: %d", l)
		}

		context = []byte(opt.Context)

		// This disallows Ed25519ctx with a 0 length context, which is
		// technically allowed by the RFC ("SHOULD NOT be empty"), but
		// is discouraged and somewhat nonsensical anyway.
		f = fCtx
	}

	return f, context, nil
}

func checkHash(f dom2Flag, message []byte, hashFunc crypto.Hash) (dom2Flag, error) {
	switch hashFunc {
	case crypto.SHA512:
		if l := len(message); l != sha512.Size {
			return f, fmt.Errorf("ed25519: bad message hash length: %d", l)
		}
		f = fPh
	case crypto.Hash(0):
	default:
		return f, fmt.Errorf("ed25519: expected opts HashFunc zero (unhashed message, for Ed25519/Ed25519ctx) or SHA-512 (for Ed25519ph)")
	}

	return f, nil
}

// VerifyOptions can be used to specify verification behavior for compatibility
// with other Ed25519 implementations.
type VerifyOptions struct {
	// AllowSmallOrderA allows signatures with a small order A.
	//
	// Note: This disables the check that makes the scheme strongly
	// binding.
	AllowSmallOrderA bool

	// AllowSmallOrder R allows signatures with a small order R.
	AllowSmallOrderR bool

	// AllowNonCanonicalAR allows signatures with a non-canonical
	// encoding of A and/or R.
	AllowNonCanonicalAR bool

	// CofactorlessVerify uses the cofactorless verification equation.
	//
	// Note: Setting this option is incompatible with batch verification.
	CofactorlessVerify bool
}

// PrivateKey is the type of Ed25519 private keys. It implements crypto.Signer.
type PrivateKey []byte

// Public returns the PublicKey corresponding to priv.
func (priv PrivateKey) Public() crypto.PublicKey {
	pub := make([]byte, PublicKeySize)
	copy(pub, priv[SeedSize:])
	return PublicKey(pub)
}

// Equal reports whether priv and x have the same value.
func (priv PrivateKey) Equal(x crypto.PrivateKey) bool {
	xx, ok := x.(PrivateKey)
	if !ok {
		return false
	}
	return bytes.Equal(priv, xx)
}

// Seed returns the private key seed corresponding to priv. It is provided for
// interoperability with RFC 8032. RFC 8032's private keys correspond to seeds
// in this package.
func (priv PrivateKey) Seed() []byte {
	s := make([]byte, SeedSize)
	copy(s, priv[:SeedSize])
	return s
}

// Sign signs the given message with priv. rand is ignored. If opts.HashFunc()
// is crypto.SHA512, the pre-hashed variant Ed25519ph is used and message is
// expected to be a SHA-512 hash, otherwise opts.HashFunc() must be
// crypto.Hash(0) and the message must not be hashed, as Ed25519 performs two
// passes over messages to be signed.
func (priv PrivateKey) Sign(rand io.Reader, message []byte, opts crypto.SignerOpts) (signature []byte, err error) {
	var (
		context []byte
		f       dom2Flag = fPure
	)
	if o, ok := opts.(*Options); ok {
		f, context, err = o.verify()
		if err != nil {
			return nil, err
		}
	}

	// Now that the Options specific validation is done, see if the caller
	// wants Ed25519ph instead.
	f, err = checkHash(f, message, opts.HashFunc())
	if err != nil {
		return nil, err
	}

	if l := len(priv); l != PrivateKeySize {
		return nil, fmt.Errorf("ed25519: bad private key length: %d", l)
	}

	var extsk [64]byte
	h := sha512.New()
	_, _ = h.Write(priv[:32])
	h.Sum(extsk[:0])
	extsk[0] &= 248
	extsk[31] &= 127
	extsk[31] |= 64

	// r = H(aExt[32..64], m)
	var (
		hashr [64]byte
		r     scalar.Scalar
	)
	h.Reset()
	writeDom2(h, f, context)
	_, _ = h.Write(extsk[32:])
	_, _ = h.Write(message)
	h.Sum(hashr[:0])
	if err = r.FromBytesModOrderWide(hashr[:]); err != nil {
		return nil, fmt.Errorf("ed25519: failed to deserialize r scalar: %w", err)
	}

	// R = rB
	var rCompressed curve.CompressedEdwardsY
	R := curve.ED25519_BASEPOINT_TABLE.Mul(&r)
	rCompressed.FromEdwardsPoint(&R)

	// S = H(R,A,m)
	var (
		hram [64]byte
		S    scalar.Scalar
	)
	h.Reset()
	writeDom2(h, f, context)
	_, _ = h.Write(rCompressed[:])
	_, _ = h.Write(priv[32:])
	_, _ = h.Write(message)
	h.Sum(hram[:0])
	if err = S.FromBytesModOrderWide(hram[:]); err != nil {
		return nil, fmt.Errorf("ed25519: failed to deserialize H(R,A,m) scalar: %w", err)
	}

	// S = H(R,A,m)a
	var a scalar.Scalar
	if err = a.FromBytesModOrder(extsk[:32]); err != nil {
		return nil, fmt.Errorf("ed25519: failed to deserialize a scalar: %w", err)
	}
	S.Mul(&S, &a)

	// S = (r + H(R,A,m)a)
	S.Add(&S, &r)

	// S = (r + H(R,A,m)a) mod L
	var RS [SignatureSize]byte
	copy(RS[:32], rCompressed[:])
	if err = S.ToBytes(RS[32:]); err != nil {
		return nil, fmt.Errorf("ed25519: failed to serialize S scalar: %w", err)
	}

	return RS[:], nil
}

// PublicKey is the type of Ed25519 public keys.
type PublicKey []byte

// Any methods implemented on PublicKey might need to also be implemented on
// PrivateKey, as the latter embeds the former and will expose its methods.

// Equal reports whether pub and x have the same value.
func (pub PublicKey) Equal(x crypto.PublicKey) bool {
	xx, ok := x.(PublicKey)
	if !ok {
		return false
	}
	return bytes.Equal(pub, xx)
}

// Sign signs the message with privateKey and returns a signature. It will
// panic if len(privateKey) is not PrivateKeySize.
func Sign(privateKey PrivateKey, message []byte) []byte {
	signature, err := privateKey.Sign(nil, message, defaultPureOptions)
	if err != nil {
		panic(err)
	}

	return signature
}

// Verify reports whether sig is a valid signature of message by publicKey. It
// will panic if len(publicKey) is not PublicKeySize.
func Verify(publicKey PublicKey, message, sig []byte) bool {
	return VerifyWithOptions(publicKey, message, sig, defaultPureOptions)
}

// VerifyWithOptions reports whether sig is a valid Ed25519 signature by
// publicKey with the extra Options to support Ed25519ph (pre-hashed by
// SHA-512) or Ed25519ctx (includes a domain separation context). It
// will panic if len(publicKey) is not PublicKeySize, len(message) is
// not sha512.Size (if pre-hashed), or len(opts.Context) is greater than
// ContextMaxSize.
func VerifyWithOptions(publicKey PublicKey, message, sig []byte, opts *Options) bool {
	ok, err := verifyWithOptionsNoPanic(publicKey, message, sig, opts)
	if err != nil {
		panic(err)
	}

	return ok
}

func verifyWithOptionsNoPanic(publicKey PublicKey, message, sig []byte, opts *Options) (bool, error) {
	f, context, err := opts.verify()
	if err != nil {
		return false, err
	}
	vOpts := opts.Verify
	if vOpts == nil {
		vOpts = defaultPureOptions.Verify
	}

	// Now that the Options specific validation is done, see if the caller
	// wants Ed25519ph instead.
	f, err = checkHash(f, message, opts.HashFunc())
	if err != nil {
		return false, err
	}

	if len(sig) != SignatureSize || (sig[63]&224 != 0) {
		return false, nil
	}

	var aCompressed curve.CompressedEdwardsY
	if err = aCompressed.FromBytes(publicKey); err != nil {
		return false, nil
	}

	var A curve.EdwardsPoint
	if err = A.FromCompressedY(&aCompressed); err != nil {
		return false, nil
	}

	// Check A order (required for strong binding).
	if !vOpts.AllowSmallOrderA && A.IsSmallOrder() {
		return false, nil
	}

	// hram = H(R,A,m)
	var (
		hash [64]byte
		hram scalar.Scalar
	)
	h := sha512.New()
	writeDom2(h, f, context)
	_, _ = h.Write(sig[:32])
	_, _ = h.Write(publicKey[:])
	_, _ = h.Write(message)
	h.Sum(hash[:0])
	if err = hram.FromBytesModOrderWide(hash[:]); err != nil {
		return false, fmt.Errorf("ed25519: failed to deserialize H(R,A,m) scalar: %w", err)
	}

	// https://tools.ietf.org/html/rfc8032#section-5.1.7 requires that s be in
	// the range [0, order) in order to prevent signature malleability.
	if !scMinimal(sig[32:]) {
		return false, nil
	}

	// R
	var rCompressed curve.CompressedEdwardsY
	_ = rCompressed.FromBytes(sig[:32]) // Can't fail.

	var checkR curve.EdwardsPoint
	if err = checkR.FromCompressedY(&rCompressed); err != nil {
		return false, nil
	}

	// Check R order.
	if !vOpts.AllowSmallOrderR && checkR.IsSmallOrder() {
		return false, nil
	}

	// Check if A and R are canonical.
	if !vOpts.AllowNonCanonicalAR && !(aCompressed.IsCanonical() && rCompressed.IsCanonical()) {
		return false, nil
	}

	// S
	var S scalar.Scalar
	if err = S.FromBytesModOrder(sig[32:]); err != nil {
		return false, fmt.Errorf("ed25519: failed to deserialize S scalar: %w", err)
	}

	// SB - H(R,A,m)A
	var R curve.EdwardsPoint
	A.Neg()
	R.DoubleScalarMulBasepointVartime(&hram, &A, &S)

	// There are 2 ways of verifying Ed25519 signatures in the wild, due
	// to the original paper/implementation, RFC, and FIPS draft.
	//
	// For the purpose of compatibility, support the old way of doing
	// things, though this is now considered unwise.
	if vOpts.CofactorlessVerify {
		return checkR.Equal(&R) == 1, nil
	}

	// Check that [8]R == [8](SB - H(R,A,m)A)).  Note that this actually
	// checks if [8](R - (SB - H(R,A,m)A) is the identity element (the
	// the difference is small order).
	var rDiff curve.EdwardsPoint
	rDiff.Sub(&R, &checkR)
	return rDiff.IsSmallOrder(), nil
}

// NewKeyFromSeed calculates a private key from a seed. It will panic if
// len(seed) is not SeedSize. This function is provided for interoperability
// with RFC 8032. RFC 8032's private keys correspond to seeds in this
// package.
func NewKeyFromSeed(seed []byte) PrivateKey {
	if l := len(seed); l != SeedSize {
		panic("ed25519: bad seed length: " + strconv.Itoa(l))
	}

	var digest [64]byte
	h := sha512.New()
	_, _ = h.Write(seed)
	h.Sum(digest[:0])

	digest[0] &= 248
	digest[31] &= 127
	digest[31] |= 64

	var a scalar.Scalar
	if err := a.FromBits(digest[:32]); err != nil {
		panic("ed25519: failed to deserialize scalar: " + err.Error())
	}

	A := curve.ED25519_BASEPOINT_TABLE.Mul(&a)

	var aCompressed curve.CompressedEdwardsY
	aCompressed.FromEdwardsPoint(&A)

	privateKey := make([]byte, 0, PrivateKeySize)
	privateKey = append(privateKey, seed...)
	privateKey = append(privateKey, aCompressed[:]...)

	return privateKey
}

// GenerateKey generates a public/private key pair using entropy from rand.
// If rand is nil, crypto/rand.Reader will be used.
func GenerateKey(rand io.Reader) (PublicKey, PrivateKey, error) {
	if rand == nil {
		rand = cryptorand.Reader
	}

	seed := make([]byte, SeedSize)
	if _, err := io.ReadFull(rand, seed); err != nil {
		return nil, nil, err
	}

	privateKey := NewKeyFromSeed(seed)
	publicKey := make([]byte, PublicKeySize)
	copy(publicKey, privateKey[32:])

	return publicKey, privateKey, nil
}

type dom2Flag byte

const (
	fCtx  dom2Flag = 0
	fPh   dom2Flag = 1
	fPure dom2Flag = 255 // Not in RFC, for implementation purposes.

	dom2Prefix = "SigEd25519 no Ed25519 collisions"
)

func writeDom2(w io.Writer, f dom2Flag, c []byte) {
	if f == fPure {
		return
	}

	cLen := len(c)
	if cLen > ContextMaxSize {
		panic("ed25519: bad context length: " + strconv.Itoa(cLen))
	}

	_, _ = w.Write([]byte(dom2Prefix))
	_, _ = w.Write([]byte{byte(f), byte(cLen)})
	_, _ = w.Write(c)
}

// order is the order of Curve25519 in little-endian form.
var order = [4]uint64{0x5812631a5cf5d3ed, 0x14def9dea2f79cd6, 0, 0x1000000000000000}

// scMinimal returns true if the given scalar is less than the order of the
// curve.
func scMinimal(scalar []byte) bool {
	if scalar[31]&240 == 0 {
		// 4 most significant bits unset, succeed fast
		return true
	}
	if scalar[31]&244 != 0 {
		// Any of the 3 most significant bits set, fail fast
		return false
	}

	// 4th most significant bit set (unlikely), actually check vs order
	for i := 3; ; i-- {
		v := binary.LittleEndian.Uint64(scalar[i*8:])
		if v > order[i] {
			return false
		} else if v < order[i] {
			break
		} else if i == 0 {
			return false
		}
	}

	return true
}