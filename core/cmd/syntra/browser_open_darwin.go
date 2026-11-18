//go:build darwin

package main

import "os/exec"

func openTrustedBrowserURL(raw string) error {
	command := exec.Command("/usr/bin/open", raw)
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
