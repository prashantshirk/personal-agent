//go:build !linux

package monitor

import "errors"

func DiskPercent(string)(int,error){return 0,errors.New("disk usage monitoring is supported on Linux")}
