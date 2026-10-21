package llog

import (
	"os"

	"github.com/sirupsen/logrus"
)

var Logger *logrus.Logger

func init() {
	Logger = logrus.New()
	Logger.SetReportCaller(true)
}

func SetLevel(level logrus.Level) {
	Logger.SetLevel(level)
}

var LoggerCni *logrus.Logger

func NewLoggerForCNI() *logrus.Logger {

	log := logrus.New()
	log.SetOutput(os.Stderr)
	log.SetReportCaller(true)

	LoggerCni = log
	return LoggerCni
}
