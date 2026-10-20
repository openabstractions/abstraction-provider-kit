//go:build windows

package modelbytes

import "golang.org/x/sys/windows"

// Inheritable marks the lease's write end inheritable and returns its
// value, for the handle list of the engine about to start (supervision
// Options.Inherit, os/exec's AdditionalInheritedHandles). Call Lent once the
// engine has started.
func (l *Lease) Inheritable() (uintptr, error) {
	h := l.w.Fd()
	if err := windows.SetHandleInformation(windows.Handle(h), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		return 0, err
	}
	return h, nil
}
