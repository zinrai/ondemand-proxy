package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// Through systemctl rather than D-Bus directly: it keeps the binary free of a
// D-Bus client, and whatever the host uses to limit what the proxy may start
// and stop applies to systemctl just the same
func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func unitFor(template, id string) string {
	return template + "@" + id + ".service"
}
