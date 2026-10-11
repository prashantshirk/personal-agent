//go:build !linux

package router

func readMem(string)string{return "unavailable on this platform"}
func diskUsage(string)string{return "unavailable on this platform"}
