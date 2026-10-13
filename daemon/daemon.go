package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"gitlab.msxf.com/mscloud/lira/pkg/llog"
	"gitlab.msxf.com/mscloud/lira/pkg/rpc"
	"gitlab.msxf.com/mscloud/lira/pkg/service"
	"gitlab.msxf.com/mscloud/lira/pkg/utils"

	"github.com/alexflint/go-filemutex"
	"github.com/go-logr/logr"
	"google.golang.org/grpc"

	"k8s.io/apimachinery/pkg/util/wait"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const lockFile = "/var/run/lira/lirad.lock"
const daemonRPCTimeout = 120 * time.Second

func newUnixListener(addr string) (net.Listener, error) {
	err := os.MkdirAll(filepath.Dir(addr), 0700)
	if err != nil {
		return nil, fmt.Errorf("error create socket dir: %s, %w", addr, err)
	}

	err = syscall.Unlink(addr)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("error unlink socket file: %s, %w", addr, err)
	}

	mask := syscallUmask(0777)
	defer syscallUmask(mask)

	l, err := net.Listen("unix", addr)
	if err != nil {
		return nil, err
	}
	err = os.Chmod(addr, 0600)
	if err != nil {
		return nil, err
	}

	return l, nil
}

func CniInterceptor(ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	ctx, cancel := context.WithTimeout(ctx, daemonRPCTimeout)
	defer cancel()

	switch r := req.(type) {
	case *rpc.AllocIPRequest:
		l := logf.FromContext(ctx, "pod", utils.PodInfoKey(r.K8SPodNamespace, r.K8SPodName), "containerID", r.K8SPodInfraContainerId)
		ctx = logr.NewContext(ctx, l)
	case *rpc.ReleaseIPRequest:
		l := logf.FromContext(ctx, "pod", utils.PodInfoKey(r.K8SPodNamespace, r.K8SPodName), "containerID", r.K8SPodInfraContainerId)
		ctx = logr.NewContext(ctx, l)
	default:
	}
	return handler(ctx, req)
}

func Run(ctx context.Context, socketFilePath string) error {
	log := llog.Logger
	err := os.MkdirAll(filepath.Dir(lockFile), 0700)
	if err != nil {
		return err
	}
	lock, err := filemutex.New(lockFile)
	if err != nil {
		return fmt.Errorf("error create lock file: %s, %w", lockFile, err)
	}
	defer func(lock *filemutex.FileMutex) {
		_ = lock.Unlock()
	}(lock)
	err = wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (done bool, err error) {
		innerErr := lock.TryLock()
		if innerErr != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("failed to acquire lock: %v", err)
	}
	l, err := newUnixListener(socketFilePath)
	if err != nil {
		return fmt.Errorf("error listen at %s: %v", socketFilePath, err)
	}
	svc, err := service.NewService(ctx)
	if err != nil {
		return err
	}

	grpcServer := grpc.NewServer()
	rpc.RegisterLiraBackendServer(grpcServer, svc)
	stop := make(chan struct{})
	go func() {
		log.Info("start serving, ", "listen on ", socketFilePath)
		err = grpcServer.Serve(l)
		if err != nil {
			log.Error(err, "error serving grpc")
			close(stop)
		}
	}()
	select {
	case <-ctx.Done():
	case <-stop:
	}
	grpcServer.Stop()
	return nil
}
