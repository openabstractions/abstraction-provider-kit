package sdcpp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// VorbisSound reads the engine's container. Any bytes answer a clip or an
// error without a panic, and a clip it answers reads back as WebM with the
// Vorbis track.
func FuzzVorbisSound(f *testing.F) {
	engine, _ := engineClip(3, false)
	silent, _ := engineClip(2, true)
	f.Add(engine)
	f.Add(silent)
	recorded := filepath.Join("testdata", "ltx-512x512-sound.webm")
	data, err := os.ReadFile(recorded)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(data)
	f.Fuzz(func(t *testing.T, data []byte) {
		out, _, err := VorbisSound(data)
		if err != nil {
			if !errors.Is(err, errSound) && !errors.Is(err, ErrNoSound) {
				t.Fatalf("an error outside the package's two: %v", err)
			}
			return
		}
		c, err := readClipAny(out)
		if err != nil {
			t.Fatal(err)
		}
		vorbis := false
		for _, codec := range c.codecs {
			vorbis = vorbis || codec == "A_VORBIS"
		}
		if c.docType != "webm" || !vorbis {
			t.Fatalf("doc type %q, codecs %v", c.docType, c.codecs)
		}
	})
}
