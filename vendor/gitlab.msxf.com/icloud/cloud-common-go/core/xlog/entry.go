package xlog

import (
	"fmt"
	"github.com/sirupsen/logrus"
	"os"
	"sync"
)

type MsEntry struct {
	rawEntries []*logrus.Entry
	lockers []*sync.RWMutex
}

func (entry *MsEntry) AddEntry(item *logrus.Entry, locker *sync.RWMutex) {
	entry.rawEntries = append(entry.rawEntries, item)
	entry.lockers = append(entry.lockers, locker)
}

func (entry *MsEntry) Log(level logrus.Level, args ...interface{}) {
	for i, en := range entry.rawEntries {
		entry.lockers[i].RLock()
		defer entry.lockers[i].RUnlock()

		en.Log(level, args...)
	}
}

func (entry *MsEntry) Logln(level logrus.Level, args ...interface{}) {
	for i, en := range entry.rawEntries {
		entry.lockers[i].RLock()
		defer entry.lockers[i].RUnlock()

		en.Logln(level, args...)
	}
}

func (entry *MsEntry) Logf(level logrus.Level, format string, args ...interface{}) {
	for i, en := range entry.rawEntries {
		entry.lockers[i].RLock()
		defer entry.lockers[i].RUnlock()

		en.Logf(level, format, args...)
	}
}

func (entry *MsEntry) Trace(args ...interface{}) {
	entry.Log(logrus.TraceLevel, args...)
}

func (entry *MsEntry) Debug(args ...interface{}) {
	entry.Log(logrus.DebugLevel, args...)
}

func (entry *MsEntry) Print(args ...interface{}) {
		entry.Info(args...)
}

func (entry *MsEntry) Info(args ...interface{}) {
	entry.Log(logrus.InfoLevel, args...)
}

func (entry *MsEntry) Warn(args ...interface{}) {
	entry.Log(logrus.WarnLevel, args...)
}

func (entry *MsEntry) Warning(args ...interface{}) {
	entry.Warn(args...)
}

func (entry *MsEntry) Error(args ...interface{}) {
	entry.Log(logrus.ErrorLevel, args...)
}

func (entry *MsEntry) Fatal(args ...interface{}) {
	entry.Log(logrus.FatalLevel, args...)
	os.Exit(1)

}

func (entry *MsEntry) Panic(args ...interface{}) {
	entry.Log(logrus.PanicLevel, args...)
	panic(fmt.Sprint(args...))

}

func (entry *MsEntry) Tracef(format string, args ...interface{}) {
	entry.Logf(logrus.TraceLevel, format, args...)

}

func (entry *MsEntry) Debugf(format string, args ...interface{}) {
	entry.Logf(logrus.DebugLevel, format, args...)

}

func (entry *MsEntry) Infof(format string, args ...interface{}) {
	entry.Logf(logrus.InfoLevel, format, args...)
}

func (entry *MsEntry) Printf(format string, args ...interface{}) {
	entry.Infof(format, args...)

}

func (entry *MsEntry) Warnf(format string, args ...interface{}) {
	entry.Logf(logrus.WarnLevel, format, args...)
}

func (entry *MsEntry) Warningf(format string, args ...interface{}) {
	entry.Warnf(format, args...)

}

func (entry *MsEntry) Errorf(format string, args ...interface{}) {
	entry.Logf(logrus.ErrorLevel, format, args...)
}

func (entry *MsEntry) Fatalf(format string, args ...interface{}) {
	entry.Logf(logrus.FatalLevel, format, args...)
		os.Exit(1)

}

func (entry *MsEntry) Panicf(format string, args ...interface{}) {
	entry.Logf(logrus.PanicLevel, format, args...)
}


func (entry *MsEntry) Traceln(args ...interface{}) {
	entry.Logln(logrus.TraceLevel, args...)
}

func (entry *MsEntry) Debugln(args ...interface{}) {
	entry.Logln(logrus.DebugLevel, args...)
}

func (entry *MsEntry) Infoln(args ...interface{}) {
	entry.Logln(logrus.InfoLevel, args...)
}

func (entry *MsEntry) Println(args ...interface{}) {
	entry.Infoln(args...)
}

func (entry *MsEntry) Warnln(args ...interface{}) {
	entry.Logln(logrus.WarnLevel, args...)
}

func (entry *MsEntry) Warningln(args ...interface{}) {
	entry.Warnln(args...)
}

func (entry *MsEntry) Errorln(args ...interface{}) {
	entry.Logln(logrus.ErrorLevel, args...)
}

func (entry *MsEntry) Fatalln(args ...interface{}) {
	entry.Logln(logrus.FatalLevel, args...)
	os.Exit(1)
}

func (entry *MsEntry) Panicln(args ...interface{}) {
	entry.Logln(logrus.PanicLevel, args...)
}
