package xlog

import (
	"fmt"
	"os"
	"syscall"
)

var (
	kernel32         = syscall.MustLoadDLL("kernel32.dll")
	procSetStdHandle = kernel32.MustFindProc("SetStdHandle")
)

func setStdHandle(stdhandle int32, handle syscall.Handle) error {
	r0, _, e1 := syscall.Syscall(procSetStdHandle.Addr(), 2, uintptr(stdhandle), uintptr(handle), 0)
	if r0 == 0 {
		if e1 != 0 {
			return error(e1)
		}
		return syscall.EINVAL
	}
	return nil
}

// redirectStderr to the file passed in
func RewriteStderrFile(path string) {
	f, err := os.OpenFile(path,
		os.O_RDWR|os.O_CREATE|os.O_APPEND, 0755)
	if err != nil {
		fmt.Printf("log open info file error:%s", err)
		return
	}
	err = setStdHandle(syscall.STD_ERROR_HANDLE, syscall.Handle(f.Fd()))
	if err != nil {
		fmt.Printf("Failed to redirect stderr to file: %v", err)
	}
	// SetStdHandle does not affect prior references to stderr
	os.Stderr = f
}
