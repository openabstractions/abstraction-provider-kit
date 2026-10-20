package vorbis

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/rand"
	"testing"
)

// bitReader reads bits least significant first, as a Vorbis decoder does.
type bitReader struct {
	buf []byte
	n   uint
}

func (r *bitReader) read(bits int) uint64 {
	var v uint64
	for i := 0; i < bits; i++ {
		if r.n/8 < uint(len(r.buf)) && r.buf[r.n/8]>>(r.n%8)&1 != 0 {
			v |= 1 << uint(i)
		}
		r.n++
	}
	return v
}

// entry reads one codeword of c by walking its tree a bit at a time.
func (r *bitReader) entry(t *testing.T, c *codebook) int {
	var code uint32
	for l := 1; l <= 32; l++ {
		code = code<<1 | uint32(r.read(1))
		for e, el := range c.lengths {
			if el == l && c.codes[e] == code {
				return e
			}
		}
	}
	t.Fatal("no codeword matched")
	return 0
}

// decode is the Vorbis I decode of this package's configuration: floor 1
// with two posts, residue type 1, the inverse MDCT, the window and overlap
// add. It answers each channel's output.
func decode(t *testing.T, s Stream, channels int) [][]float64 {
	b, err := newBooks()
	if err != nil {
		t.Fatal(err)
	}
	out := make([][]float64, channels)
	previous := make([][]float64, channels)
	for k, packet := range s.Packets {
		r := &bitReader{buf: packet}
		if r.read(1) != 0 || r.read(1) != 1 || r.read(1) != 1 {
			t.Fatalf("packet %d header", k)
		}
		floor := make([]float64, channels)
		used := make([]bool, channels)
		for c := range floor {
			if r.read(1) == 1 {
				y0, y1 := r.read(8), r.read(8)
				if y0 != y1 {
					t.Fatalf("packet %d: a sloped floor", k)
				}
				floor[c], used[c] = inverseDB[y0], true
			}
		}
		residue := make([][]float64, channels)
		for c := range residue {
			residue[c] = make([]float64, half)
		}
		for p := 0; p < half/partition; p++ {
			classes := make([]int, channels)
			for c := range classes {
				if used[c] {
					classes[c] = r.entry(t, b.class)
				}
			}
			for c := range classes {
				if used[c] && classes[c] == 1 {
					for i := 0; i < partition; i++ {
						residue[c][p*partition+i] += float64(r.entry(t, b.value) - maxStep)
					}
				}
			}
		}
		for c := 0; c < channels; c++ {
			block := make([]float64, BlockSamples)
			for n := range block {
				var sum float64
				for i := 0; i < half; i++ {
					sum += floor[c] * residue[c][i] * math.Cos(2*math.Pi/BlockSamples*(float64(n)+0.5+BlockSamples/4)*(float64(i)+0.5))
				}
				block[n] = sum * window[n]
			}
			if previous[c] != nil {
				for n := 0; n < half; n++ {
					out[c] = append(out[c], previous[c][half+n]+block[n])
				}
			}
			previous[c] = block
		}
	}
	return out
}

// The stream decodes, by the specification's steps, to the samples it was
// made from: a tone, noise and a silent stretch in stereo, within the
// quantization's error and at their sample positions.
func TestEncodedStreamDecodesToTheSamples(t *testing.T) {
	const rate, length = 48000, 6000
	random := rand.New(rand.NewSource(1))
	left, right := make([]float32, length), make([]float32, length)
	for i := range left {
		left[i] = float32(0.5 * math.Sin(2*math.Pi*440*float64(i)/rate))
		if i < 4000 {
			right[i] = float32(0.3 * (random.Float64()*2 - 1))
		}
	}
	s, err := Encode([][]float32{left, right}, rate)
	if err != nil {
		t.Fatal(err)
	}
	if want := (length+half-1)/half + 1; len(s.Packets) != want {
		t.Fatalf("%d packets, want %d", len(s.Packets), want)
	}
	out := decode(t, s, 2)
	if len(out[0]) < length || len(out[0]) >= length+half {
		t.Fatalf("decoded %d samples for %d", len(out[0]), length)
	}
	for c, in := range [][]float32{left, right} {
		var signal, noise float64
		for i, x := range in {
			signal += float64(x) * float64(x)
			d := out[c][i] - float64(x)
			noise += d * d
		}
		if snr := 10 * math.Log10(signal/noise); snr < 35 {
			t.Fatalf("channel %d decodes at %.1f dB SNR", c, snr)
		}
	}
	for i := 5120; i < length; i++ {
		if out[1][i] != 0 {
			t.Fatalf("the silent stretch decodes to %v at %d", out[1][i], i)
		}
	}
}

// The headers are what Vorbis I §4.2 asks: the identification header names
// the channels, rate and block sizes, and every codebook's tree is complete
// with no codeword over 32 bits.
func TestHeadersAndCodebooks(t *testing.T) {
	s, err := Encode([][]float32{{0.1, 0.2}}, 44100)
	if err != nil {
		t.Fatal(err)
	}
	id := s.Headers[0]
	if len(id) != 30 || id[0] != 1 || string(id[1:7]) != "vorbis" || id[11] != 1 ||
		binary.LittleEndian.Uint32(id[12:16]) != 44100 || id[28] != longExp<<4|shortExp || id[29] != 1 {
		t.Fatalf("identification header % x", id)
	}
	if s.Headers[1][0] != 3 || !bytes.Contains(s.Headers[1], []byte(Vendor)) || s.Headers[2][0] != 5 {
		t.Fatal("comment or setup header type")
	}
	b, err := newBooks()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range b.value.lengths {
		if l > 32 {
			t.Fatalf("a %d-bit codeword", l)
		}
	}
	if _, err := makeWords([]int{1, 2}); err == nil {
		t.Fatal("an underpopulated tree was accepted")
	}
	if _, err := makeWords([]int{1, 1, 1}); err == nil {
		t.Fatal("an overpopulated tree was accepted")
	}
	if _, err := Encode([][]float32{{0}, {0, 0}}, 48000); err == nil {
		t.Fatal("channels of different lengths were encoded")
	}
}
