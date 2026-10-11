//go:build linux

package router

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

func readMem(key string) string {
	b,err:=os.ReadFile("/proc/meminfo");if err!=nil{return "unavailable"}
	for _,line:=range strings.Split(string(b),"\n"){fields:=strings.Fields(line);if len(fields)>=2&&strings.TrimSuffix(fields[0],":")==key{n,_:=strconv.ParseInt(fields[1],10,64);return strconv.FormatInt(n/1024,10)+" MiB"}}
	return "unavailable"
}
func diskUsage(path string)string{var s syscall.Statfs_t;if err:=syscall.Statfs(path,&s);err!=nil{return "unavailable"};total:=s.Blocks*uint64(s.Bsize);free:=s.Bavail*uint64(s.Bsize);if total==0{return "unavailable"};used:=100*(total-free)/total;return strconv.FormatUint(used,10)+"% used"}
