// Package refusal is the typed refusal a provider gives a load or a request
// its engine cannot take, and the words the kit's own parts refuse with
// (CONTRACT.md KIT-R1, KIT-R2). A refusal carries a word a caller reads and a
// detail that stays in the provider's log; it never carries engine text.
package refusal

import (
	"errors"
	"fmt"
	"strings"
	"time"

	res "github.com/openabstractions/abstraction-resource/go/client"
	"github.com/openabstractions/abstraction-resource/go/gpufault"
	"github.com/openabstractions/abstraction-resource/go/holder"
)

// The words the kit's parts refuse with.
const (
	// NoEngine is an engine that is not configured, not the pinned one, or
	// refused to load.
	NoEngine = "engine:none"
	// Unavailable is a load that failed for a reason no other word names.
	Unavailable = "unavailable"
	// EngineUnavailable is the word a video or image whose engine could not
	// load fails with when the load named no word of its own.
	EngineUnavailable = "engine:unavailable"
	// EnginePeer is an engine whose socket is served by a process outside its
	// contained tree (abstraction-identity ID-T8).
	EnginePeer = "engine:peer"
	// EngineSandbox is an engine whose sandbox is not installed, is being
	// installed, updated or removed, or lacks a grant the launch needs
	// (KIT-S3); the engine never starts uncontained instead.
	EngineSandbox = "engine:sandbox"
	// EngineUnreachable is an engine whose endpoint every connect failed to
	// reach for enginehttp.UnreachableFor while it loaded, with an error a
	// starting engine never gives (KIT-P5).
	EngineUnreachable = "engine:unreachable"
)

// A Refusal is a typed refusal: the word a caller reads and the detail the
// provider's log keeps.
type Refusal struct{ Word, Detail string }

func (r *Refusal) Error() string {
	if r.Detail == "" {
		return r.Word
	}
	return r.Word + ": " + r.Detail
}

// New is a refusal with word and a formatted detail.
func New(word, format string, args ...any) *Refusal {
	return &Refusal{Word: word, Detail: fmt.Sprintf(format, args...)}
}

// WordOf is err's refusal word, or "" when err carries none.
func WordOf(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Word
	}
	return ""
}

// Load is the refusal of a holder load of name on resource for amount bytes
// (KIT-R2): a refusal the load made itself as it is, the lease's own word
// (holder.Failure.Word) with the detail its outcome calls for, and
// Unavailable for the rest.
func Load(resource, name string, amount int64, err error) error {
	var refusal *Refusal
	if errors.As(err, &refusal) {
		return refusal
	}
	var failure *holder.Failure
	if !errors.As(err, &failure) {
		return New(Unavailable, "%v", err)
	}
	switch failure.Stage {
	case holder.StageAcquire:
		switch word := failure.Word(); word {
		case holder.WordEnded:
			return New(word, "the resource service ended the lease while the engine was starting")
		case holder.WordNotPermitted:
			return New(word, "no rule lets this provider hold %s", resource)
		case holder.WordInsufficient:
			return New(word, "%s has less than %s's %d-byte file-size admission estimate", resource, name, amount)
		case holder.WordHoldersRefused:
			return New(word, "%s", asked(failure.Result))
		}
		return New(Unavailable, "the lease service did not answer: %v", failure)
	case holder.StageStart:
		return New(Unavailable, "the engine would not start: %v", failure.Err)
	default:
		return New(Unavailable, "%v", failure.Err)
	}
}

// asked names each holder the service asked and its answer.
func asked(result res.AcquireResult) string {
	if len(result.Asked) == 0 {
		return "no holder was asked"
	}
	parts := make([]string, 0, len(result.Asked))
	for _, row := range result.Asked {
		parts = append(parts, row.Holder+" "+row.Answer)
	}
	return strings.Join(parts, "; ")
}

// LoadWord is the word a video or image whose engine could not load fails
// with: the load's own word, or EngineUnavailable.
func LoadWord(err error) string {
	word := WordOf(err)
	if word == "" || word == Unavailable {
		word = EngineUnavailable
	}
	return word
}

// GPU is a GPU fault refusal as a provider's refusal, so a caller reads
// gpu:standby, gpu:resetting or gpu:quarantined rather than unavailable
// (resource RES-G3..RES-G5). Any other error is returned as it is.
func GPU(err error) error {
	var refusal *gpufault.Refusal
	if errors.As(err, &refusal) {
		detail := refusal.Detail
		if !refusal.Until.IsZero() {
			detail += "; until " + refusal.Until.Format(time.RFC3339)
		}
		return &Refusal{Word: refusal.Word, Detail: detail}
	}
	return err
}
