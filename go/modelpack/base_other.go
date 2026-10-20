//go:build !windows

package modelpack

import (
	"errors"

	"github.com/openabstractions/abstraction-provider-kit/go/modelfiles"
)

// borrowBase reports errors.ErrUnsupported: sections are built on Windows
// only (INF-MS6).
func borrowBase(*modelfiles.Section) (ProvenBase, func(), error) {
	return ProvenBase{}, nil, errors.ErrUnsupported
}
