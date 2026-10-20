// Package vorbis encodes PCM as a Vorbis I stream: the three header packets
// and one audio packet per block, as a WebM or Ogg muxer carries them. It
// writes one fixed configuration of the Vorbis I specification (Xiph.Org,
// 2020-07-04): 2048-sample blocks only, a flat floor 1 per block and channel,
// and residue type 1 whose values are the block's MDCT coefficients
// quantized uniformly to at most 255 steps of the floor. Every decoder of the
// specification plays it; the files are larger than a tuned encoder's.
package vorbis

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// ErrInput reports samples this package does not encode.
var ErrInput = errors.New("vorbis: the samples are not encodable")

// The fixed configuration.
const (
	// BlockSamples is the block length; each audio packet after the first
	// adds BlockSamples/2 samples per channel.
	BlockSamples = 2048
	half         = BlockSamples / 2
	shortExp     = 8  // blocksize_0, declared and unused by the one mode
	longExp      = 11 // blocksize_1 = BlockSamples
	rangeBits    = 10 // floor 1 x range: half
	partition    = 32 // residue partition size
	maxStep      = 255
	// MaxChannels and the rate bounds are what Encode accepts.
	MaxChannels = 8
	MinRate     = 8000
	MaxRate     = 192000
	// Vendor is the comment header's vendor string.
	Vendor = "OpenAbstractions Vorbis"
)

// minStepIndex keeps the floor at or above about -110 dBFS: a block quieter
// than that encodes as silence.
const minStepIndex = 25

// Stream is one encoded stream: Headers are the identification, comment and
// setup packets, and Packets the audio packets in order. Packet k holds the
// block centred on sample k·BlockSamples/2; a decoder outputs nothing for
// packet 0 and samples [(k-1)·BlockSamples/2, k·BlockSamples/2) for packet k.
type Stream struct {
	Headers [3][]byte
	Packets [][]byte
}

// PacketStart is the first sample a decoder outputs for packet k.
func PacketStart(k int) int64 {
	if k < 1 {
		return 0
	}
	return int64(k-1) * half
}

// codebook is one codebook: its codeword lengths and the codewords the
// specification's tree assignment gives them, most significant bit first.
type codebook struct {
	lengths []int
	codes   []uint32
}

func newCodebook(lengths []int) (*codebook, error) {
	codes, err := makeWords(lengths)
	if err != nil {
		return nil, err
	}
	return &codebook{lengths: lengths, codes: codes}, nil
}

// put writes entry's codeword, its first tree bit first.
func (c *codebook) put(w *bitWriter, entry int) {
	l := c.lengths[entry]
	code := c.codes[entry]
	for b := l - 1; b >= 0; b-- {
		w.bit(code >> uint(b) & 1)
	}
}

// makeWords assigns codewords to lengths in entry order, each the lowest
// free codeword of its length (Vorbis I §3.2.1), and refuses lengths whose
// tree is over- or underpopulated.
func makeWords(lengths []int) ([]uint32, error) {
	var marker [33]uint32
	codes := make([]uint32, len(lengths))
	for i, l := range lengths {
		if l < 1 || l > 32 {
			return nil, fmt.Errorf("vorbis: codeword length %d", l)
		}
		entry := marker[l]
		if l < 32 && entry>>uint(l) != 0 {
			return nil, errors.New("vorbis: the codebook tree is overpopulated")
		}
		codes[i] = entry
		for j := l; j > 0; j-- {
			if marker[j]&1 != 0 {
				if j == 1 {
					marker[1]++
				} else {
					marker[j] = marker[j-1] << 1
				}
				break
			}
			marker[j]++
		}
		for j := l + 1; j < 33; j++ {
			if marker[j]>>1 != entry {
				break
			}
			entry = marker[j]
			marker[j] = marker[j-1] << 1
		}
	}
	for i := 1; i < 33; i++ {
		if marker[i]&(0xffffffff>>uint(32-i)) != 0 {
			return nil, errors.New("vorbis: the codebook tree is underpopulated")
		}
	}
	return codes, nil
}

// huffmanLengths are the code lengths of a Huffman code for weights.
func huffmanLengths(weights []float64) []int {
	type node struct {
		weight float64
		leaves []int
	}
	nodes := make([]node, len(weights))
	for i, w := range weights {
		nodes[i] = node{w, []int{i}}
	}
	lengths := make([]int, len(weights))
	for len(nodes) > 1 {
		sort.SliceStable(nodes, func(a, b int) bool { return nodes[a].weight < nodes[b].weight })
		a, b := nodes[0], nodes[1]
		for _, leaf := range a.leaves {
			lengths[leaf]++
		}
		for _, leaf := range b.leaves {
			lengths[leaf]++
		}
		merged := node{a.weight + b.weight, append(append([]int{}, a.leaves...), b.leaves...)}
		nodes = append([]node{merged}, nodes[2:]...)
	}
	return lengths
}

// books are the stream's two codebooks: the residue classbook (entry c is
// classification c) and the value book (entry e is the value e-maxStep).
type books struct {
	class, value *codebook
}

func newBooks() (books, error) {
	class, err := newCodebook([]int{1, 1})
	if err != nil {
		return books{}, err
	}
	weights := make([]float64, 2*maxStep+1)
	for i := range weights {
		// A Laplacian over the step values, floored so that no codeword
		// passes 32 bits.
		weights[i] = math.Exp(-math.Abs(float64(i-maxStep))/40)*(1<<20) + 64
	}
	value, err := newCodebook(huffmanLengths(weights))
	if err != nil {
		return books{}, err
	}
	return books{class: class, value: value}, nil
}

// bitWriter packs bits least significant first, as Vorbis I §2 does.
type bitWriter struct {
	buf []byte
	n   uint
}

func (w *bitWriter) bit(b uint32) {
	if w.n%8 == 0 {
		w.buf = append(w.buf, 0)
	}
	if b&1 != 0 {
		w.buf[len(w.buf)-1] |= 1 << (w.n % 8)
	}
	w.n++
}

func (w *bitWriter) write(v uint64, bits int) {
	for i := 0; i < bits; i++ {
		w.bit(uint32(v >> uint(i) & 1))
	}
}

func (w *bitWriter) bytes(s string) {
	for i := 0; i < len(s); i++ {
		w.write(uint64(s[i]), 8)
	}
}

// packFloat is Vorbis I §9.2.2's float32 for an integer of at most 21 bits.
func packFloat(v int) uint32 {
	var sign uint32
	if v < 0 {
		sign, v = 0x80000000, -v
	}
	return sign | 788<<21 | uint32(v)
}

func identification(channels, rate int) []byte {
	var w bitWriter
	w.write(1, 8)
	w.bytes("vorbis")
	w.write(0, 32)
	w.write(uint64(channels), 8)
	w.write(uint64(rate), 32)
	w.write(0, 32)
	w.write(0, 32)
	w.write(0, 32)
	w.write(shortExp, 4)
	w.write(longExp, 4)
	w.write(1, 1)
	return w.buf
}

func comment() []byte {
	var w bitWriter
	w.write(3, 8)
	w.bytes("vorbis")
	w.write(uint64(len(Vendor)), 32)
	w.bytes(Vendor)
	w.write(0, 32)
	w.write(1, 1)
	return w.buf
}

func setup(b books) []byte {
	var w bitWriter
	w.write(5, 8)
	w.bytes("vorbis")
	w.write(2-1, 8)
	putBook := func(c *codebook, lookup bool) {
		w.write(0x564342, 24)
		w.write(1, 16)
		w.write(uint64(len(c.lengths)), 24)
		w.write(0, 1) // ordered
		w.write(0, 1) // sparse
		for _, l := range c.lengths {
			w.write(uint64(l-1), 5)
		}
		if !lookup {
			w.write(0, 4)
			return
		}
		// Lookup type 1 of dimension 1: entry e is minimum + e·delta.
		w.write(1, 4)
		w.write(uint64(packFloat(-maxStep)), 32)
		w.write(uint64(packFloat(1)), 32)
		const valueBits = 9
		w.write(valueBits-1, 4)
		w.write(0, 1) // sequence_p
		for e := range c.lengths {
			w.write(uint64(e), valueBits)
		}
	}
	putBook(b.class, false)
	putBook(b.value, true)
	w.write(0, 6) // one time-domain transform
	w.write(0, 16)
	w.write(0, 6) // one floor
	w.write(1, 16)
	w.write(0, 5) // no partitions: posts at 0 and 2^rangeBits only
	w.write(0, 2) // multiplier 1
	w.write(rangeBits, 4)
	w.write(0, 6)  // one residue
	w.write(1, 16) // type 1
	w.write(0, 24)
	w.write(half, 24)
	w.write(partition-1, 24)
	w.write(2-1, 6) // classifications: 0 silent, 1 coded
	w.write(0, 8)   // classbook
	w.write(0, 3)   // class 0 cascade: none
	w.write(0, 1)
	w.write(1, 3) // class 1 cascade: pass 0
	w.write(0, 1)
	w.write(1, 8)  // class 1, pass 0: the value book
	w.write(0, 6)  // one mapping
	w.write(0, 16) // type 0
	w.write(0, 1)  // one submap
	w.write(0, 1)  // no coupling
	w.write(0, 2)
	w.write(0, 8) // time
	w.write(0, 8) // floor 0
	w.write(0, 8) // residue 0
	w.write(0, 6) // one mode
	w.write(1, 1) // long blocks
	w.write(0, 16)
	w.write(0, 16)
	w.write(0, 8)
	w.write(1, 1) // framing
	return w.buf
}

// inverseDB is floor 1's amplitude table (Vorbis I §10.1): 256 steps from
// 1.0649863e-07 to 1.0.
var inverseDB = func() [256]float64 {
	var t [256]float64
	low := 1.0649863e-07
	for i := range t {
		t[i] = low * math.Pow(1/low, float64(i)/255)
	}
	return t
}()

// window is the Vorbis window of a long block between long blocks.
var window = func() []float64 {
	w := make([]float64, BlockSamples)
	for n := range w {
		s := math.Sin((float64(n) + 0.5) / BlockSamples * math.Pi)
		w[n] = math.Sin(math.Pi / 2 * s * s)
	}
	return w
}()

// mdct is the forward transform a Vorbis decoder inverts:
// X[k] = 4/N · Σ x[n] cos(2π/N · (n + 1/2 + N/4) · (k + 1/2)).
type mdct struct{ cos []float32 }

func newMDCT() *mdct {
	c := make([]float32, half*BlockSamples)
	for k := 0; k < half; k++ {
		for n := 0; n < BlockSamples; n++ {
			c[k*BlockSamples+n] = float32(math.Cos(2 * math.Pi / BlockSamples * (float64(n) + 0.5 + BlockSamples/4) * (float64(k) + 0.5)))
		}
	}
	return &mdct{cos: c}
}

func (m *mdct) forward(in []float64, out []float64) {
	for k := 0; k < half; k++ {
		row := m.cos[k*BlockSamples : (k+1)*BlockSamples]
		var sum float64
		for n, x := range in {
			sum += x * float64(row[n])
		}
		out[k] = sum * 4 / BlockSamples
	}
}

// Encode encodes channels of equal length at rate. Samples are in [-1, 1].
func Encode(samples [][]float32, rate int) (Stream, error) {
	channels := len(samples)
	if channels < 1 || channels > MaxChannels || rate < MinRate || rate > MaxRate {
		return Stream{}, fmt.Errorf("%w: %d channels at %d Hz", ErrInput, channels, rate)
	}
	length := len(samples[0])
	for _, ch := range samples {
		if len(ch) != length {
			return Stream{}, fmt.Errorf("%w: channels of different lengths", ErrInput)
		}
	}
	if length == 0 {
		return Stream{}, fmt.Errorf("%w: no samples", ErrInput)
	}
	b, err := newBooks()
	if err != nil {
		return Stream{}, err
	}
	s := Stream{Headers: [3][]byte{identification(channels, rate), comment(), setup(b)}}
	m := newMDCT()
	frame := make([]float64, BlockSamples)
	coeffs := make([][]float64, channels)
	for c := range coeffs {
		coeffs[c] = make([]float64, half)
	}
	steps := make([]int, channels)
	values := make([][]int, channels)
	for c := range values {
		values[c] = make([]int, half)
	}
	last := (length + half - 1) / half
	for k := 0; k <= last; k++ {
		start := (k - 1) * half
		for c, ch := range samples {
			for n := range frame {
				i := start + n
				x := 0.0
				if i >= 0 && i < length {
					x = float64(ch[i])
					if math.IsNaN(x) {
						x = 0
					}
					x = math.Max(-1, math.Min(1, x))
				}
				frame[n] = x * window[n]
			}
			m.forward(frame, coeffs[c])
			steps[c] = quantize(coeffs[c], values[c])
		}
		s.Packets = append(s.Packets, audioPacket(b, steps, values))
	}
	return s, nil
}

// quantize sets values to coeffs in steps of the floor it picks and answers
// the floor's table index, or -1 for a block that is silent.
func quantize(coeffs []float64, values []int) int {
	peak := 0.0
	for _, x := range coeffs {
		peak = math.Max(peak, math.Abs(x))
	}
	index := sort.Search(256, func(i int) bool { return inverseDB[i] >= peak/maxStep })
	index = max(min(index, 255), minStepIndex)
	step := inverseDB[index]
	any := false
	for i, x := range coeffs {
		q := int(math.Round(x / step))
		q = max(-maxStep, min(maxStep, q))
		values[i] = q
		any = any || q != 0
	}
	if !any {
		return -1
	}
	return index
}

// audioPacket writes one long block's packet (Vorbis I §4.3).
func audioPacket(b books, steps []int, values [][]int) []byte {
	var w bitWriter
	w.write(0, 1) // audio
	w.write(1, 1) // previous block long
	w.write(1, 1) // next block long
	for _, step := range steps {
		if step < 0 {
			w.write(0, 1)
			continue
		}
		w.write(1, 1)
		w.write(uint64(step), 8)
		w.write(uint64(step), 8)
	}
	for p := 0; p < half/partition; p++ {
		classes := make([]int, len(steps))
		for c, step := range steps {
			if step < 0 {
				continue
			}
			for _, v := range values[c][p*partition : (p+1)*partition] {
				if v != 0 {
					classes[c] = 1
					break
				}
			}
			b.class.put(&w, classes[c])
		}
		for c, step := range steps {
			if step < 0 || classes[c] == 0 {
				continue
			}
			for _, v := range values[c][p*partition : (p+1)*partition] {
				b.value.put(&w, v+maxStep)
			}
		}
	}
	return w.buf
}
