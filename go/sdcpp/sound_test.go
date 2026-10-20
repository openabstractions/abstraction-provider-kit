package sdcpp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

// engineClip builds a container in the layout master-843's sd-server
// writes: EBML header with DocType matroska, a segment with info, a VP8
// track 1 and, unless silent, a PCM track 2 (A_PCM/INT/LIT, 48 kHz stereo,
// 16-bit), and one cluster per frame holding the frame and its 2000 sample
// frames of a 440 Hz tone.
func engineClip(frames int, silent bool) ([]byte, [][]byte) {
	header := el(idEBML, uintEl(idEBMLVersion, 1), uintEl(idEBMLReadVer, 1), uintEl(idEBMLMaxIDLen, 4),
		uintEl(idEBMLMaxSizeLen, 8), el(idDocType, []byte("matroska")), uintEl(idDocTypeVersion, 4), uintEl(idDocTypeReadVer, 2))
	f32 := make([]byte, 4)
	binary.BigEndian.PutUint32(f32, math.Float32bits(float32(frames*1000/24)))
	info := el(idInfo, uintEl(idTimecodeScale, 1000000), el(idDuration, f32))
	video := el(idTrackEntry, uintEl(idTrackNumber, 1), uintEl(idTrackUID, 7), uintEl(idTrackType, 1), el(idCodecID, []byte("V_VP8")),
		el(0xE0, uintEl(0xB0, 512), uintEl(0xBA, 512)))
	rate := make([]byte, 4)
	binary.BigEndian.PutUint32(rate, math.Float32bits(48000))
	audio := el(idTrackEntry, uintEl(idTrackNumber, 2), uintEl(idTrackUID, 9), uintEl(idTrackType, 2), el(idCodecID, []byte("A_PCM/INT/LIT")),
		el(idAudio, el(idSamplingFreq, rate), uintEl(idChannels, 2), uintEl(idBitDepth, 16)))
	tracks := el(idTracks, video)
	if !silent {
		tracks = el(idTracks, video, audio)
	}
	var clusters bytes.Buffer
	var payloads [][]byte
	sample := 0
	for f := 0; f < frames; f++ {
		frame := bytes.Repeat([]byte{byte(f), 0x9d, 0x01, 0x2a}, 64)
		payloads = append(payloads, frame)
		at := int64(f * 1000 / 24)
		parts := [][]byte{uintEl(idTimecode, uint64(at)), simpleBlock(block{track: 1, time: at, keyframe: f%10 == 0, payload: frame}, at)}
		if !silent {
			pcm := make([]byte, 0, 2000*4)
			for i := 0; i < 2000; i++ {
				v := int16(12000 * math.Sin(2*math.Pi*440*float64(sample)/48000))
				pcm = binary.LittleEndian.AppendUint16(pcm, uint16(v))
				pcm = binary.LittleEndian.AppendUint16(pcm, uint16(v/2))
				sample++
			}
			parts = append(parts, simpleBlock(block{track: 2, time: at, keyframe: true, payload: pcm}, at))
		}
		clusters.Write(el(idCluster, parts...))
	}
	segment := el(idSegment, info, tracks, clusters.Bytes())
	return append(header, segment...), payloads
}

// sd-server's PCM sound is rewritten as a WebM Vorbis track: DocType webm,
// the video track and every frame as the engine wrote them, keyframe cues,
// and Vorbis headers in Xiph lacing.
func TestVorbisSoundRewritesThePCMTrack(t *testing.T) {
	engine, frames := engineClip(25, false)
	out, sound, err := VorbisSound(engine)
	if err != nil {
		t.Fatal(err)
	}
	if sound.Channels != 2 || sound.Rate != 48000 || sound.Samples != 25*2000 {
		t.Fatalf("sound %+v", sound)
	}
	c, err := readClipAny(out)
	if err != nil {
		t.Fatal(err)
	}
	if c.docType != "webm" || len(c.codecs) != 2 || c.codecs[1] != "V_VP8" || c.codecs[2] != "A_VORBIS" {
		t.Fatalf("doc type %q, codecs %v", c.docType, c.codecs)
	}
	if len(c.blocks[1]) != len(frames) {
		t.Fatalf("%d video blocks for %d frames", len(c.blocks[1]), len(frames))
	}
	for i, b := range c.blocks[1] {
		if !bytes.Equal(b.payload, frames[i]) || b.time != int64(i*1000/24) || b.keyframe != (i%10 == 0) {
			t.Fatalf("video block %d: %+v", i, b)
		}
	}
	// 50000 samples are 49 decoded blocks of 1024 plus the first packet.
	if n := len(c.blocks[2]); n != (50000+1023)/1024+1 {
		t.Fatalf("%d audio packets", n)
	}
	private := c.private[2]
	if len(private) < 3 || private[0] != 2 || !bytes.Contains(private, []byte("\x01vorbis")) || !bytes.Contains(private, []byte("\x05vorbis")) {
		t.Fatalf("CodecPrivate % x", private[:min(16, len(private))])
	}
	if c.cues != 3 {
		t.Fatalf("%d cues for 3 keyframes", c.cues)
	}
}

// A clip without an audio track answers ErrNoSound, and a malformed one
// another error; the caller keeps the engine's bytes either way.
func TestVorbisSoundLeavesOtherClips(t *testing.T) {
	silent, _ := engineClip(3, true)
	if _, _, err := VorbisSound(silent); !errors.Is(err, ErrNoSound) {
		t.Fatalf("a silent clip answered %v", err)
	}
	engine, _ := engineClip(3, false)
	if _, _, err := VorbisSound(engine[:len(engine)-10]); err == nil || errors.Is(err, ErrNoSound) {
		t.Fatalf("a truncated clip answered %v", err)
	}
	// Video WebM does not carry is left in the engine's container.
	other := bytes.Replace(engine, []byte("V_VP8"), []byte("V_MP4"), 1)
	if _, _, err := VorbisSound(other); err == nil || errors.Is(err, ErrNoSound) {
		t.Fatalf("a clip of V_MP4 video answered %v", err)
	}
}

// parsedClip is what the test reads back from a written container.
type parsedClip struct {
	docType string
	codecs  map[uint64]string
	private map[uint64][]byte
	blocks  map[uint64][]block
	cues    int
}

func readClipAny(data []byte) (parsedClip, error) {
	p := parsedClip{codecs: map[uint64]string{}, private: map[uint64][]byte{}, blocks: map[uint64][]block{}}
	top, err := readElements(data)
	if err != nil {
		return p, err
	}
	header, err := readElements(top[0].body)
	if err != nil {
		return p, err
	}
	for _, e := range header {
		if e.id == idDocType {
			p.docType = string(e.body)
		}
	}
	children, err := readElements(top[1].body)
	if err != nil {
		return p, err
	}
	for _, child := range children {
		switch child.id {
		case idTracks:
			entries, _ := readElements(child.body)
			for _, entry := range entries {
				fields, _ := readElements(entry.body)
				var number uint64
				for _, f := range fields {
					if f.id == idTrackNumber {
						number = readUint(f.body)
					}
				}
				for _, f := range fields {
					switch f.id {
					case idCodecID:
						p.codecs[number] = string(f.body)
					case idCodecPrivate:
						p.private[number] = f.body
					}
				}
			}
		case idCluster:
			parts, _ := readElements(child.body)
			var at int64
			for _, part := range parts {
				switch part.id {
				case idTimecode:
					at = int64(readUint(part.body))
				case idSimpleBlock:
					b, err := readBlock(part.body, at, true)
					if err != nil {
						return p, err
					}
					p.blocks[b.track] = append(p.blocks[b.track], b)
				}
			}
		case idCues:
			points, _ := readElements(child.body)
			p.cues = len(points)
		}
	}
	return p, nil
}
