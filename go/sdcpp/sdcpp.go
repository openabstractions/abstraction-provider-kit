// Package sdcpp composes shared mandatory image safety with engine execution policy.
package sdcpp

import (
	"errors"
	media "github.com/openabstractions/abstraction-inference/go/mediationcontent"
	"strconv"
)

const (
	ReasonEngineDirective = media.ReasonEngineDirective
	ReasonImageSize       = media.ReasonImageSize
	ReasonRequestForm     = media.ReasonRequestForm
	ReasonVideoBound      = "video:engine-bound"
	ReasonFirstFrame      = "frame:engine-bound"
	MinDimension          = media.MinDimension
	MaxDimension          = media.MaxDimension
	MaxPixels             = media.MaxPixels
)

func HasDirective(text string) bool                        { return media.HasDirective(text) }
func CheckText(field string, text string) error            { return media.CheckText(field, text) }
func CheckSize(width int, height int) error                { return media.CheckSize(width, height) }
func CheckOpenAISize(size string) error                    { return media.CheckOpenAISize(size) }
func CheckImageInput(kind string, data []byte) error       { return media.CheckImageInput(kind, data) }
func InputSize(kind string, data []byte) (int, int, error) { return media.InputSize(kind, data) }
func Reason(err error) string {
	if reason := media.Reason(err); reason != "" {
		return reason
	}
	switch {
	case errors.Is(err, ErrVideoRequest):
		return ReasonVideoBound
	case errors.Is(err, ErrFirstFrame):
		return ReasonFirstFrame
	}
	return ""
}

// decimal parses 1..5 ASCII digits.
func decimal(s string) (int, bool) {
	if s == "" || len(s) > 5 {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}
