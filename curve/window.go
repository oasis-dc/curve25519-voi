// Copyright (c) 2016-2019 Isis Agora Lovecruft, Henry de Valence. All rights reserved.
// Copyright (c) 2020-2021 Oasis Labs Inc.  All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are
// met:
//
// 1. Redistributions of source code must retain the above copyright
// notice, this list of conditions and the following disclaimer.
//
// 2. Redistributions in binary form must reproduce the above copyright
// notice, this list of conditions and the following disclaimer in the
// documentation and/or other materials provided with the distribution.
//
// 3. Neither the name of the copyright holder nor the names of its
// contributors may be used to endorse or promote products derived from
// this software without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS
// IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED
// TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A
// PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
// HOLDER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
// SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED
// TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR
// PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF
// LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING
// NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
// SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

package curve

import "github.com/oasisprotocol/curve25519-voi/internal/subtle"

// This has a mountain of duplicated code because having generics is too
// much to ask for currently.

type projectiveNielsPointLookupTable [8]projectiveNielsPoint

func (tbl *projectiveNielsPointLookupTable) Lookup(x int8) projectiveNielsPoint {
	// Compute xabs = |x|
	xmask := x >> 7
	xabs := uint8((x + xmask) ^ xmask)

	// Set t = 0 * P = identity
	var t projectiveNielsPoint
	t.Identity()
	for j := 1; j < 9; j++ {
		// Copy `points[j-1] == j*P` onto `t` in constant time if `|x| == j`.
		c := subtle.ConstantTimeCompareByte(byte(xabs), byte(j))
		t.ConditionalAssign(&tbl[j-1], c)
	}
	// Now t == |x| * P.

	negMask := int(byte(xmask & 1))
	t.ConditionalNegate(negMask)
	// Now t == x * P.

	return t
}

func newProjectiveNielsPointLookupTable(ep *EdwardsPoint) projectiveNielsPointLookupTable {
	var epPNiels projectiveNielsPoint
	epPNiels.SetEdwards(ep)

	points := [8]projectiveNielsPoint{
		epPNiels, epPNiels, epPNiels, epPNiels,
		epPNiels, epPNiels, epPNiels, epPNiels,
	}
	for j := 0; j < 7; j++ {
		var (
			tmp  completedPoint
			tmp2 EdwardsPoint
		)
		points[j+1].SetEdwards(tmp2.setCompleted(tmp.AddEdwardsProjectiveNiels(ep, &points[j])))
	}

	return projectiveNielsPointLookupTable(points)
}

// Note: Unlike curve25519-dalek, the table uses the packed format as the
// internal representation, as 96-byte entries are significantly easier
// to manipulate with vector instructions.
type packedAffineNielsPointLookupTable [8][96]byte

func (tbl *packedAffineNielsPointLookupTable) Lookup(x int8) affineNielsPoint {
	// Compute xabs = |x|
	xmask := x >> 7
	xabs := uint8((x + xmask) ^ xmask)

	// Set t = 0 * P = identity
	var tPacked [96]byte
	lookupPackedAffineNiels(tbl, &tPacked, xabs)
	// Now t == |x| * P.

	// Unpack t.
	var t affineNielsPoint
	_, _ = t.y_plus_x.SetBytes(tPacked[0:32])
	_, _ = t.y_minus_x.SetBytes(tPacked[32:64])
	_, _ = t.xy2d.SetBytes(tPacked[64:96])

	negMask := int(byte(xmask & 1))
	t.ConditionalNegate(negMask)
	// Now t == x * P.

	return t
}

func lookupPackedAffineNielsGeneric(table *packedAffineNielsPointLookupTable, out *[96]byte, xabs uint8) {
	*out = identityAffineNielsPacked
	for j := 1; j < 9; j++ {
		// Copy `points[j-1] == j*P` onto `t` in constant time if `|x| == j`.
		c := subtle.ConstantTimeCompareByte(byte(xabs), byte(j))
		subtle.MoveConditionalBytesx96(out, &table[j-1], uint64(c))
	}
}

func newPackedAffineNielsPointLookupTable(ep *EdwardsPoint) packedAffineNielsPointLookupTable {
	var epANiels affineNielsPoint
	epANiels.SetEdwards(ep)

	points := [8]affineNielsPoint{
		epANiels, epANiels, epANiels, epANiels,
		epANiels, epANiels, epANiels, epANiels,
	}
	for j := 0; j < 7; j++ {
		var (
			tmp  completedPoint
			tmp2 EdwardsPoint
		)
		points[j+1].SetEdwards(tmp2.setCompleted(tmp.AddEdwardsAffineNiels(ep, &points[j])))
	}

	// Pack the table.  At this point, `points` is equivalent to the table
	// used by curve25519-dalek.
	var tbl packedAffineNielsPointLookupTable
	for i, point := range points {
		_ = point.y_plus_x.ToBytes(tbl[i][0:32])
		_ = point.y_minus_x.ToBytes(tbl[i][32:64])
		_ = point.xy2d.ToBytes(tbl[i][64:96])
	}

	return tbl
}

// Holds odd multiples 1A, 3A, ..., 15A of a point A.
type projectiveNielsPointNafLookupTable [8]projectiveNielsPoint

func (tbl *projectiveNielsPointNafLookupTable) lookup(x uint8) *projectiveNielsPoint {
	return &tbl[x/2]
}

func newProjectiveNielsPointNafLookupTable(ep *EdwardsPoint) projectiveNielsPointNafLookupTable {
	var epPNiels projectiveNielsPoint
	epPNiels.SetEdwards(ep)

	Ai := [8]projectiveNielsPoint{
		epPNiels, epPNiels, epPNiels, epPNiels,
		epPNiels, epPNiels, epPNiels, epPNiels,
	}

	var A2 EdwardsPoint
	A2.double(ep)
	for i := 0; i < 7; i++ {
		var (
			tmp  completedPoint
			tmp2 EdwardsPoint
		)
		Ai[i+1].SetEdwards(tmp2.setCompleted(tmp.AddEdwardsProjectiveNiels(&A2, &Ai[i])))
	}

	return projectiveNielsPointNafLookupTable(Ai)
}

// Holds stuff up to 8.
type affineNielsPointNafLookupTable [64]affineNielsPoint

func (tbl *affineNielsPointNafLookupTable) lookup(x uint8) *affineNielsPoint {
	return &tbl[x/2]
}

func newAffineNielsPointNafLookupTable(ep *EdwardsPoint) affineNielsPointNafLookupTable { //nolint:unused,deadcode
	var epANiels affineNielsPoint
	epANiels.SetEdwards(ep)

	var Ai [64]affineNielsPoint
	for i := range Ai {
		Ai[i] = epANiels
	}

	var A2 EdwardsPoint
	A2.double(ep)

	for i := 0; i < 63; i++ {
		var (
			tmp  completedPoint
			tmp2 EdwardsPoint
		)
		Ai[i+1].SetEdwards(tmp2.setCompleted(tmp.AddEdwardsAffineNiels(&A2, &Ai[i])))
	}

	return affineNielsPointNafLookupTable(Ai)
}
