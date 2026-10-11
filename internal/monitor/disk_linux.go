//go:build linux

package monitor

import "syscall"

func DiskPercent(path string)(int,error){var stat syscall.Statfs_t;if err:=syscall.Statfs(path,&stat);err!=nil{return 0,err};total:=stat.Blocks*uint64(stat.Bsize);free:=stat.Bavail*uint64(stat.Bsize);if total==0{return 0,nil};return int((total-free)*100/total),nil}
