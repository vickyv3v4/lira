package main

import (
	"flag"
	"os"

	"github.com/sirupsen/logrus"
	ctrl "sigs.k8s.io/controller-runtime"

	"gitlab.msxf.com/mscloud/lira/daemon"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"
	"gitlab.msxf.com/mscloud/lira/pkg/types"
	"gitlab.msxf.com/mscloud/lira/pkg/utils"
)

var (
	logLevel string
)

func main() {
	var levelNum logrus.Level
	utils.NewGConfig()

	fs := flag.NewFlagSet("lira", flag.ExitOnError)
	fs.StringVar(&logLevel, "log-level", "info", "lira log level")
	var runMode string
	flag.StringVar(&runMode, "run-mode", string(utils.MultiENIIP), "lira ip allocate mode")
	utils.GConfig.RunMode = utils.LiraIPMode(runMode)

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
