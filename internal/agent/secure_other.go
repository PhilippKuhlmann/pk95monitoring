//go:build !windows

package agent

// secureDir is a no-op: Save creates the directory with mode 0700 and the file with 0600.
func secureDir(string) error { return nil }
