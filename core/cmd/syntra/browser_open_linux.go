//go:build linux

package main

import "os/exec"

func openTrustedBrowserURL(raw string) error {
	command := exec.Command("xdg-open", raw)
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
