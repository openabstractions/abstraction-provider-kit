// Package runtimeprog names the runtime programs a provider's endpoint
// admits: fixed by the provider's installation, never by the process that
// connects (KIT-E1).
package runtimeprog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	identity "github.com/openabstractions/abstraction-identity"
)

// Trusted is configured when it names programs, else the installed runtime
// beside the running provider (both forms on Windows). An explicit path
// supports an isolated runtime installation and test fixtures.
func Trusted(configured []string) ([]string, error) {
	if len(configured) > 0 {
		return At("", configured)
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return At(self, nil)
}

// At is configured when it names programs, else the runtime beside self.
// Each must be a clean absolute path of a regular file; an installed form
// that is absent is skipped.
func At(self string, configured []string) ([]string, error) {
	var names []string
	if len(configured) > 0 {
		names = configured
	} else {
		dir := filepath.Dir(self)
		name := "openabstractions"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		names = append(names, filepath.Join(dir, name))
		if runtime.GOOS == "windows" {
			names = append(names, filepath.Join(dir, "openabstractionsw.exe"))
		}
	}
	var programs []string
	for _, name := range names {
		if !identity.ValidSubjectProgram(name) || !filepath.IsAbs(name) {
			return nil, fmt.Errorf("--runtime-program must be a clean absolute executable path")
		}
		info, err := os.Stat(name)
		if err != nil {
			if len(configured) == 0 && os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("runtime program %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("runtime program %s is not a regular file", name)
		}
		programs = append(programs, identity.CanonicalProgramPath(name))
	}
	if len(programs) == 0 {
		return nil, errors.New("no installed runtime program beside this provider")
	}
	return programs, nil
}
