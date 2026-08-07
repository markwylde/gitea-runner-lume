//go:build windows

package guestagent

import "errors"

func PrepareWorkspace(string) error { return errors.New("guest agent requires a Unix guest") }
