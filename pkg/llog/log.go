package llog

import "github.com/sirupsen/logrus"

var Logger *logrus.Logger

func init() {
	Logger = logrus.New()
	Logger.SetReportCaller(true)
}

func SetLevel(level logrus.Level) {
	Logger.SetLevel(level)
}
