//go:build !linux

package term

import "errors"

func startShell(ShellSpec) (Shell, error) {
	return nil, errors.New("term: PTYs are Linux-only")
}
