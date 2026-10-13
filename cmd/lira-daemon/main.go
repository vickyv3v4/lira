package main

import (
	"flag"
	"os"

	"github.com/sirupsen/logrus"
	ctrl "sigs.k8s.io/controller-runtime"

	"gitlab.msxf.com/mscloud/lira/daemon"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"
	"gitlab.msxf.com/mscloud/lira/pkg/types"
)

var (
	logLevel string
)

func main() {
	var levelNum logrus.Level

	fs := flag.NewFlagSet("lira", flag.ExitOnError)
	fs.StringVar(&logLevel, "log-level", "info", "lira log level")

	err := fs.Parse(os.Args[1:])
	if err != nil {
		panic(err)
	}
	if levelNum, err = logrus.ParseLevel(logLevel); err != nil {
		panic(err)
	}
	llog.Logger.SetLevel(levelNum)
	ctx := ctrl.SetupSignalHandler()

	llog.Logger.Info("start daemon service.")
	err = daemon.Run(ctx, types.DefaultSocketPath)
	if err != nil {
		llog.Logger.Fatal(err)
	}
}
