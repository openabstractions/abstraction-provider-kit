// Package modelbytes is the lender's side of model bytes (provider kit
// CONTRACT.md, "Model bytes", KIT-MB1..KIT-MB6; VISION 2026-10-01;
// research/model-bytes/DECISION.md): the fd: URI an engine OA launches
// receives in place of a model path, and the lease pipe whose closing ends
// the engine's reference.
//
// The receivers are oa_model_bytes.h, the C reference implementation, and
// oa_model_bytes.py, the Python one, both embedded here so a provider can
// stage them beside its engine. Each passes the conformance suite in this
// package's tests.
package modelbytes

import (
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// CReceiver is oa_model_bytes.h, the single-file C receiver engines vendor.
//
//go:embed oa_model_bytes.h
var CReceiver []byte

// PythonReceiver is oa_model_bytes.py, the standard-library Python receiver.
//
//go:embed oa_model_bytes.py
var PythonReceiver []byte

// URI is one model bytes URI (KIT-MB1).
type URI struct {
	// Handle is the descriptor's value in the engine: the inherited value,
	// or the value a duplication into the engine returned.
	Handle uint64
	Offset uint64
	Length uint64
	// SHA256 is the object's digest as OA proved it, 64 lower-case hex
	// digits, or empty.
	SHA256 string
	// Lease is the lease pipe end's value in the engine, or zero for none.
	Lease uint64
	// Memory marks bytes OA filled into a sealed memory object.
	Memory bool
}

// ErrURI is a string the receivers refuse as a model bytes URI.
var ErrURI = errors.New("modelbytes: not a model bytes URI")

// String spells u in the order the contract writes it.
func (u URI) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "fd:%d?offset=%d&length=%d", u.Handle, u.Offset, u.Length)
	if u.SHA256 != "" {
		b.WriteString("&sha256=" + u.SHA256)
	}
	if u.Lease != 0 {
		fmt.Fprintf(&b, "&lease=%d", u.Lease)
	}
	if u.Memory {
		b.WriteString("&kind=memory")
	}
	return b.String()
}

// DigestHex turns storage's "sha256:<hex>" form into the URI's hex form.
func DigestHex(digest string) (string, error) {
	h, ok := strings.CutPrefix(digest, "sha256:")
	if raw, err := hex.DecodeString(h); !ok || err != nil || len(raw) != 32 || strings.ToLower(h) != h {
		return "", fmt.Errorf("modelbytes: digest %q is not sha256:<64 lower-case hex>", digest)
	}
	return h, nil
}

// IsURI reports whether arg is a model bytes URI; any other string is a path.
func IsURI(arg string) bool { return strings.HasPrefix(arg, "fd:") }

// Parse reads arg as the receivers do: decimal numbers with no sign and no
// leading zero, offset and length present, length above zero, no unknown or
// repeated parameter, and offset+length within 64 bits.
func Parse(arg string) (URI, error) {
	var u URI
	rest, ok := strings.CutPrefix(arg, "fd:")
	if !ok {
		return u, ErrURI
	}
	head, query, ok := strings.Cut(rest, "?")
	if !ok {
		return u, fmt.Errorf("%w: no parameters", ErrURI)
	}
	var err error
	if u.Handle, err = number(head); err != nil {
		return u, err
	}
	seen := map[string]bool{}
	for _, part := range strings.Split(query, "&") {
		key, value, ok := strings.Cut(part, "=")
		if !ok || seen[key] {
			return u, fmt.Errorf("%w: %q", ErrURI, part)
		}
		seen[key] = true
		switch key {
		case "offset":
			u.Offset, err = number(value)
		case "length":
			u.Length, err = number(value)
		case "lease":
			u.Lease, err = number(value)
		case "sha256":
			if raw, e := hex.DecodeString(value); e != nil || len(raw) != 32 || strings.ToLower(value) != value {
				err = fmt.Errorf("%w: sha256", ErrURI)
			}
			u.SHA256 = value
		case "kind":
			if value != "memory" {
				err = fmt.Errorf("%w: kind %q", ErrURI, value)
			}
			u.Memory = true
		default:
			err = fmt.Errorf("%w: unknown parameter %q", ErrURI, key)
		}
		if err != nil {
			return u, err
		}
	}
	if !seen["offset"] || !seen["length"] || u.Length == 0 || u.Offset > ^uint64(0)-u.Length {
		return u, fmt.Errorf("%w: offset and length", ErrURI)
	}
	return u, nil
}

func number(s string) (uint64, error) {
	if s == "" || len(s) > 20 || (s[0] == '0' && len(s) > 1) || strings.TrimLeft(s, "0123456789") != "" {
		return 0, fmt.Errorf("%w: number %q", ErrURI, s)
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: number %q", ErrURI, s)
	}
	return v, nil
}
