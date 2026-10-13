package xlog
import (
	"fmt"
	"os"
	"runtime"
	"syscall"
)
var stdErrFileHandler *os.File
func RewriteStderrFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		fmt.Printf("Failed to OpenFile stderr file: %v", err)
		return err
	}
	stdErrFileHandler = file //把文件句柄保存到全局变量，避免被GC回收
	// linux重定向系统调用函数：复制一个现存的文件描述符
	if err = syscall.Dup2(int(file.Fd()), int(os.Stderr.Fd())); err != nil {
		fmt.Printf("Failed to Dup2 stderr to file: %v", err)
		return err
	}
	// 内存回收前关闭文件描述符
	runtime.SetFinalizer(stdErrFileHandler, func(fd *os.File) {
		fd.Close()
	})

	return nil
}
