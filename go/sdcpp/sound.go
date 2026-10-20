package sdcpp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/openabstractions/abstraction-provider-kit/go/vorbis"
)

// sd-server muxes an LTX clip's sound into its WebM as 16-bit PCM
// (A_PCM/INT/LIT, DocType matroska). WebM players accept Vorbis and Opus;
// Media Foundation, which plays WebM in Windows' Media Player, refuses the
// PCM track (MF_E_INVALIDMEDIATYPE), and the clip plays silent. VorbisSound
// rewrites the container with the same video blocks and the sound encoded
// as Vorbis.

// ErrNoSound reports a container that carries no audio track.
var ErrNoSound = errors.New("sd.cpp: the clip has no audio track")

// errSound reports a container VorbisSound leaves as it is.
var errSound = errors.New("sd.cpp: the clip's sound cannot be re-encoded")

// maxSoundPCMBytes bounds the PCM VorbisSound encodes: about 87 s of 48 kHz
// stereo.
const maxSoundPCMBytes = 16 << 20

// maxClipMillis bounds the times VorbisSound reads: one day in milliseconds.
const maxClipMillis = 24 * 60 * 60 * 1000

// Sound describes a re-encoded clip's audio.
type Sound struct {
	Channels, Rate int
	Samples        int
}

// Seconds is the sound's length.
func (s Sound) Seconds() float64 { return float64(s.Samples) / float64(s.Rate) }

// EBML and Matroska element IDs, marker bits included.
const (
	idEBML           = 0x1A45DFA3
	idSegment        = 0x18538067
	idSeekHead       = 0x114D9B74
	idSeek           = 0x4DBB
	idSeekID         = 0x53AB
	idSeekPosition   = 0x53AC
	idInfo           = 0x1549A966
	idTimecodeScale  = 0x2AD7B1
	idDuration       = 0x4489
	idMuxingApp      = 0x4D80
	idWritingApp     = 0x5741
	idTracks         = 0x1654AE6B
	idTrackEntry     = 0xAE
	idTrackNumber    = 0xD7
	idTrackUID       = 0x73C5
	idTrackType      = 0x83
	idCodecID        = 0x86
	idCodecPrivate   = 0x63A2
	idAudio          = 0xE1
	idSamplingFreq   = 0xB5
	idChannels       = 0x9F
	idBitDepth       = 0x6264
	idCluster        = 0x1F43B675
	idTimecode       = 0xE7
	idSimpleBlock    = 0xA3
	idBlockGroup     = 0xA0
	idBlock          = 0xA1
	idCues           = 0x1C53BB6B
	idCuePoint       = 0xBB
	idCueTime        = 0xB3
	idCueTrackPos    = 0xB7
	idCueTrack       = 0xF7
	idCueClusterPos  = 0xF1
	idEBMLVersion    = 0x4286
	idEBMLReadVer    = 0x42F7
	idEBMLMaxIDLen   = 0x42F2
	idEBMLMaxSizeLen = 0x42F3
	idDocType        = 0x4282
	idDocTypeVersion = 0x4287
	idDocTypeReadVer = 0x4285
)

type element struct {
	id   uint32
	body []byte
}

// readElements splits b into sibling elements. An unknown size, a size past
// the end and an ID or size longer than EBML allows are refused.
func readElements(b []byte) ([]element, error) {
	var out []element
	for len(b) > 0 {
		idLen := 1
		for idLen <= 4 && b[0]&(0x80>>uint(idLen-1)) == 0 {
			idLen++
		}
		if idLen > 4 || len(b) < idLen {
			return nil, fmt.Errorf("%w: a malformed element ID", errSound)
		}
		var id uint32
		for _, c := range b[:idLen] {
			id = id<<8 | uint32(c)
		}
		size, sizeLen, err := readVint(b[idLen:])
		if err != nil {
			return nil, err
		}
		start := idLen + sizeLen
		if size > uint64(len(b)-start) {
			return nil, fmt.Errorf("%w: an element runs past its parent", errSound)
		}
		out = append(out, element{id, b[start : start+int(size)]})
		b = b[start+int(size):]
	}
	return out, nil
}

// readVint reads an EBML size; the reserved all-ones value is refused.
func readVint(b []byte) (uint64, int, error) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, fmt.Errorf("%w: a malformed size", errSound)
	}
	n := 1
	for b[0]&(0x80>>uint(n-1)) == 0 {
		n++
	}
	if len(b) < n {
		return 0, 0, fmt.Errorf("%w: a malformed size", errSound)
	}
	v := uint64(b[0]) & (0xFF >> uint(n))
	for _, c := range b[1:n] {
		v = v<<8 | uint64(c)
	}
	if v == 1<<(7*uint(n))-1 {
		return 0, 0, fmt.Errorf("%w: an element of unknown size", errSound)
	}
	return v, n, nil
}

func readUint(b []byte) uint64 {
	var v uint64
	for _, c := range b[:min(len(b), 8)] {
		v = v<<8 | uint64(c)
	}
	return v
}

func readFloat(b []byte) float64 {
	switch len(b) {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b)))
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b))
	}
	return 0
}

// block is one frame of a track at an absolute time in milliseconds.
type block struct {
	track    uint64
	time     int64
	keyframe bool
	payload  []byte
}

// readBlock reads a SimpleBlock or Block body; laced blocks are refused.
func readBlock(body []byte, cluster int64, simple bool) (block, error) {
	track, n, err := readVint(body)
	if err != nil || len(body) < n+3 {
		return block{}, fmt.Errorf("%w: a malformed block", errSound)
	}
	flags := body[n+2]
	if flags&0x06 != 0 {
		return block{}, fmt.Errorf("%w: a laced block", errSound)
	}
	return block{track: track, time: cluster + int64(int16(binary.BigEndian.Uint16(body[n:]))),
		keyframe: !simple || flags&0x80 != 0, payload: body[n+3:]}, nil
}

// clip is what VorbisSound reads from sd-server's container.
type clip struct {
	duration         float64
	video            element
	videoTrack       uint64
	audioTrack       uint64
	audioUID         []byte
	rate, channels   int
	videoBlocks, pcm []block
}

func readClip(data []byte) (clip, error) {
	top, err := readElements(data)
	if err != nil {
		return clip{}, err
	}
	if len(top) < 2 || top[0].id != idEBML || top[1].id != idSegment {
		return clip{}, fmt.Errorf("%w: not one EBML header and segment", errSound)
	}
	children, err := readElements(top[1].body)
	if err != nil {
		return clip{}, err
	}
	var c clip
	var bitDepth int
	audioCodec, videoCodec := "", ""
	for _, child := range children {
		switch child.id {
		case idInfo:
			info, err := readElements(child.body)
			if err != nil {
				return clip{}, err
			}
			for _, e := range info {
				switch e.id {
				case idTimecodeScale:
					if readUint(e.body) != 1000000 {
						return clip{}, fmt.Errorf("%w: a timecode scale other than 1 ms", errSound)
					}
				case idDuration:
					// A duration that is not a finite time in range is
					// left out; the writer derives one from the blocks.
					if d := readFloat(e.body); d > 0 && d <= maxClipMillis {
						c.duration = d
					}
				}
			}
		case idTracks:
			tracks, err := readElements(child.body)
			if err != nil {
				return clip{}, err
			}
			for _, entry := range tracks {
				if entry.id != idTrackEntry {
					continue
				}
				fields, err := readElements(entry.body)
				if err != nil {
					return clip{}, err
				}
				var number, kind uint64
				var codec string
				var uid []byte
				var audio []element
				for _, f := range fields {
					switch f.id {
					case idTrackNumber:
						number = readUint(f.body)
					case idTrackType:
						kind = readUint(f.body)
					case idCodecID:
						codec = string(f.body)
					case idTrackUID:
						uid = f.body
					case idAudio:
						if audio, err = readElements(f.body); err != nil {
							return clip{}, err
						}
					}
				}
				switch {
				case kind == 1 && c.videoTrack == 0:
					c.video, c.videoTrack, videoCodec = entry, number, codec
				case kind == 2 && c.audioTrack == 0:
					c.audioTrack, c.audioUID, audioCodec = number, uid, codec
					for _, a := range audio {
						switch a.id {
						case idSamplingFreq:
							c.rate = int(readFloat(a.body))
						case idChannels:
							c.channels = int(readUint(a.body))
						case idBitDepth:
							bitDepth = int(readUint(a.body))
						}
					}
				}
			}
		case idCluster:
			parts, err := readElements(child.body)
			if err != nil {
				return clip{}, err
			}
			var at int64
			for _, p := range parts {
				var b block
				switch p.id {
				case idTimecode:
					at = int64(readUint(p.body))
					continue
				case idSimpleBlock:
					b, err = readBlock(p.body, at, true)
				case idBlockGroup:
					group, gerr := readElements(p.body)
					if gerr != nil {
						return clip{}, gerr
					}
					found := false
					for _, g := range group {
						if g.id == idBlock {
							b, err = readBlock(g.body, at, false)
							found = true
						}
					}
					if !found {
						continue
					}
				default:
					continue
				}
				if err != nil {
					return clip{}, err
				}
				if b.time < 0 || b.time > maxClipMillis {
					return clip{}, fmt.Errorf("%w: a block at %d ms", errSound, b.time)
				}
				switch {
				case b.track == c.videoTrack && c.videoTrack != 0:
					c.videoBlocks = append(c.videoBlocks, b)
				case b.track == c.audioTrack && c.audioTrack != 0:
					c.pcm = append(c.pcm, b)
				}
			}
		}
	}
	switch {
	case c.videoTrack == 0 || c.videoTrack > 126 || len(c.videoBlocks) == 0:
		return clip{}, fmt.Errorf("%w: no video track", errSound)
	case videoCodec != "V_VP8" && videoCodec != "V_VP9" && videoCodec != "V_AV1":
		// The rewrite is labelled WebM, which carries these video codecs.
		return clip{}, fmt.Errorf("%w: the video is %s", errSound, videoCodec)
	case c.audioTrack == 0:
		return clip{}, ErrNoSound
	case c.audioTrack > 126 || c.audioTrack == c.videoTrack:
		return clip{}, fmt.Errorf("%w: track number %d", errSound, c.audioTrack)
	case audioCodec != "A_PCM/INT/LIT" || bitDepth != 16:
		return clip{}, fmt.Errorf("%w: the audio is %s at %d bits", errSound, audioCodec, bitDepth)
	case c.channels < 1 || c.channels > vorbis.MaxChannels || c.rate < vorbis.MinRate || c.rate > vorbis.MaxRate:
		return clip{}, fmt.Errorf("%w: %d channels at %d Hz", errSound, c.channels, c.rate)
	}
	return c, nil
}

// VorbisSound answers sd-server's WebM with its PCM sound encoded as Vorbis
// and the video blocks unchanged. ErrNoSound reports a clip without an
// audio track; any error leaves the clip as the engine wrote it.
func VorbisSound(data []byte) ([]byte, Sound, error) {
	c, err := readClip(data)
	if err != nil {
		return nil, Sound{}, err
	}
	sort.SliceStable(c.pcm, func(i, j int) bool { return c.pcm[i].time < c.pcm[j].time })
	frame := 2 * c.channels
	total := 0
	for _, b := range c.pcm {
		total += len(b.payload)
	}
	if total > maxSoundPCMBytes {
		return nil, Sound{}, fmt.Errorf("%w: %d bytes of sound", errSound, total)
	}
	if total < frame {
		return nil, Sound{}, ErrNoSound
	}
	pcm := make([]byte, 0, total)
	for _, b := range c.pcm {
		pcm = append(pcm, b.payload...)
	}
	samples := len(pcm) / frame
	channels := make([][]float32, c.channels)
	for ch := range channels {
		channels[ch] = make([]float32, samples)
		for i := range channels[ch] {
			channels[ch][i] = float32(int16(binary.LittleEndian.Uint16(pcm[i*frame+2*ch:]))) / 32768
		}
	}
	stream, err := vorbis.Encode(channels, c.rate)
	if err != nil {
		return nil, Sound{}, fmt.Errorf("%w: %v", errSound, err)
	}
	var audio []block
	for k, p := range stream.Packets {
		audio = append(audio, block{track: c.audioTrack, time: vorbis.PacketStart(k) * 1000 / int64(c.rate), keyframe: true, payload: p})
	}
	return writeClip(c, stream.Headers, audio), Sound{Channels: c.channels, Rate: c.rate, Samples: samples}, nil
}

// EBML writing.

func putID(b *bytes.Buffer, id uint32) {
	switch {
	case id >= 1<<24:
		b.Write([]byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)})
	case id >= 1<<16:
		b.Write([]byte{byte(id >> 16), byte(id >> 8), byte(id)})
	case id >= 1<<8:
		b.Write([]byte{byte(id >> 8), byte(id)})
	default:
		b.WriteByte(byte(id))
	}
}

func putSize(b *bytes.Buffer, n uint64) {
	l := 1
	for n >= 1<<(7*uint(l))-1 {
		l++
	}
	v := n | 1<<(7*uint(l))
	for i := l - 1; i >= 0; i-- {
		b.WriteByte(byte(v >> (8 * uint(i))))
	}
}

func el(id uint32, body ...[]byte) []byte {
	var b bytes.Buffer
	n := 0
	for _, p := range body {
		n += len(p)
	}
	putID(&b, id)
	putSize(&b, uint64(n))
	for _, p := range body {
		b.Write(p)
	}
	return b.Bytes()
}

func uintEl(id uint32, v uint64) []byte {
	n := 1
	for n < 8 && v>>(8*uint(n)) != 0 {
		n++
	}
	body := make([]byte, n)
	for i := range body {
		body[i] = byte(v >> (8 * uint(n-1-i)))
	}
	return el(id, body)
}

func floatEl(id uint32, v float64) []byte {
	body := make([]byte, 8)
	binary.BigEndian.PutUint64(body, math.Float64bits(v))
	return el(id, body)
}

func idBytes(id uint32) []byte {
	var b bytes.Buffer
	putID(&b, id)
	return b.Bytes()
}

// xiphLacing is Matroska's A_VORBIS CodecPrivate: the packet count less one
// and the first two sizes in Xiph lacing, then the three headers.
func xiphLacing(headers [3][]byte) []byte {
	out := []byte{2}
	for _, h := range headers[:2] {
		n := len(h)
		for ; n >= 255; n -= 255 {
			out = append(out, 255)
		}
		out = append(out, byte(n))
	}
	for _, h := range headers {
		out = append(out, h...)
	}
	return out
}

func simpleBlock(b block, cluster int64) []byte {
	flags := byte(0)
	if b.keyframe {
		flags = 0x80
	}
	rel := uint16(int16(b.time - cluster))
	return el(idSimpleBlock, []byte{0x80 | byte(b.track), byte(rel >> 8), byte(rel), flags}, b.payload)
}

// writeClip writes a WebM: a seek head, info, the video track as the engine
// wrote it and the Vorbis track, clusters that start at each video keyframe,
// and cues for those keyframes.
func writeClip(c clip, headers [3][]byte, audio []block) []byte {
	events := append(append([]block{}, c.videoBlocks...), audio...)
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].time != events[j].time {
			return events[i].time < events[j].time
		}
		return events[i].track == c.videoTrack && events[j].track != c.videoTrack
	})
	duration := c.duration
	last := events[len(events)-1].time
	if v := c.videoBlocks; len(v) > 1 {
		last = max(last, v[len(v)-1].time+v[len(v)-1].time-v[len(v)-2].time)
	}
	if duration <= 0 {
		duration = float64(last)
	}

	uid := c.audioUID
	if len(uid) == 0 || len(uid) > 8 {
		uid = []byte{byte(c.audioTrack)}
	}
	audioEntry := el(idTrackEntry, uintEl(idTrackNumber, c.audioTrack), el(idTrackUID, uid), uintEl(idTrackType, 2),
		el(idCodecID, []byte("A_VORBIS")), el(idCodecPrivate, xiphLacing(headers)),
		el(idAudio, floatEl(idSamplingFreq, float64(c.rate)), uintEl(idChannels, uint64(c.channels))))
	info := el(idInfo, uintEl(idTimecodeScale, 1000000), floatEl(idDuration, duration),
		el(idMuxingApp, []byte("OpenAbstractions")), el(idWritingApp, []byte("OpenAbstractions")))
	tracks := el(idTracks, el(idTrackEntry, c.video.body), audioEntry)

	type cluster struct {
		time   int64
		key    bool
		blocks [][]byte
	}
	var clusters []cluster
	for _, e := range events {
		key := e.track == c.videoTrack && e.keyframe
		if len(clusters) == 0 || key || e.time-clusters[len(clusters)-1].time > 30000 {
			clusters = append(clusters, cluster{time: e.time, key: key})
		}
		cl := &clusters[len(clusters)-1]
		cl.blocks = append(cl.blocks, simpleBlock(e, cl.time))
	}

	seekEntry := func(id uint32, pos uint64) []byte {
		p := make([]byte, 8)
		binary.BigEndian.PutUint64(p, pos)
		return el(idSeek, el(idSeekID, idBytes(id)), el(idSeekPosition, p))
	}
	seekLen := len(el(idSeekHead, seekEntry(idInfo, 0), seekEntry(idTracks, 0), seekEntry(idCues, 0)))
	pos := uint64(seekLen + len(info) + len(tracks))
	var body bytes.Buffer
	var cues [][]byte
	var clusterBytes bytes.Buffer
	for _, cl := range clusters {
		if cl.key {
			cues = append(cues, el(idCuePoint, uintEl(idCueTime, uint64(cl.time)),
				el(idCueTrackPos, uintEl(idCueTrack, c.videoTrack), uintEl(idCueClusterPos, pos+uint64(clusterBytes.Len())))))
		}
		parts := append([][]byte{uintEl(idTimecode, uint64(cl.time))}, cl.blocks...)
		clusterBytes.Write(el(idCluster, parts...))
	}
	cuesAt := pos + uint64(clusterBytes.Len())
	body.Write(el(idSeekHead, seekEntry(idInfo, uint64(seekLen)), seekEntry(idTracks, uint64(seekLen+len(info))), seekEntry(idCues, cuesAt)))
	body.Write(info)
	body.Write(tracks)
	body.Write(clusterBytes.Bytes())
	body.Write(el(idCues, cues...))

	header := el(idEBML, uintEl(idEBMLVersion, 1), uintEl(idEBMLReadVer, 1), uintEl(idEBMLMaxIDLen, 4),
		uintEl(idEBMLMaxSizeLen, 8), el(idDocType, []byte("webm")), uintEl(idDocTypeVersion, 4), uintEl(idDocTypeReadVer, 2))
	var out bytes.Buffer
	out.Write(header)
	putID(&out, idSegment)
	segmentSize := make([]byte, 8)
	binary.BigEndian.PutUint64(segmentSize, uint64(body.Len())|1<<56)
	out.Write(segmentSize)
	out.Write(body.Bytes())
	return out.Bytes()
}
