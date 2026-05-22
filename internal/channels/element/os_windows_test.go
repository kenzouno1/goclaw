//go:build !sqliteonly && windows

package element

func isWindows() bool { return true }
