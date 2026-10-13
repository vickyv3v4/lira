package xlog

import (
	"context"
	"fmt"
	"github.com/sirupsen/logrus"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	LogTypeDefault            = "app"
	LogFileTimeFormat         = "2006-01-02"
	DefaultFileSizeThreshHold = 512 //Mi
	DefaultCheckFileClicker   = 5   //seconds
)

const (
	RollTypeDay RollType = 0
)

type RollType int

var LOG = Logger()

type LogBackend struct {
	FilePath string
	FileName string
	//最大文件大小（单位Ki）
	MaxFileSize int

	//滚动时间间隔, 目前默认隔日切
	//RollIntervalType RollType

	beginTime time.Time

	Cancel context.CancelFunc
	Logrus *logrus.Logger

	Locker sync.RWMutex
}

func (be *LogBackend) InitRotateAfterInitLogrus() {
	if be.Logrus.Out == os.Stdout || be.Logrus.Out == os.Stderr {
		return
	}

	if be.FilePath == "" || be.FileName == "" {
		if fd, ok := be.Logrus.Out.(*os.File); ok {
			be.FilePath = path.Dir(fd.Name())
			be.FileName = path.Base(fd.Name())
		}
	}

	if be.MaxFileSize <= 0 {
		be.MaxFileSize = DefaultFileSizeThreshHold
	}

	be.beginTime = time.Now()

	var ctx context.Context
	ctx, be.Cancel = context.WithCancel(context.Background())

	go be.doRotate(ctx)
}

func (be *LogBackend) checkTimeInterval() bool {
	now := time.Now()
	nYear, nMonth, nDay := now.Date()
	oYear, oMonth, oDay := be.beginTime.Date()

	return nYear > oYear || nMonth > oMonth || nDay > oDay
}

func (be *LogBackend) checkFileSize() bool {
	if fd, ok := be.Logrus.Out.(*os.File); ok {
		finfo, _ := fd.Stat()
		if finfo.Size() > int64(be.MaxFileSize*1024*1024) {
			return true
		}
	}
	return false
}

func (be *LogBackend) moveFile(rotateTime bool) error {
	if rotateTime {
		be.beginTime = time.Now()
	}
	logFiltTimeTail := be.beginTime.Format(LogFileTimeFormat)
	filePrefix := fmt.Sprintf("%s.%s.", be.FileName, logFiltTimeTail)

	dir, err := os.Open(be.FilePath)
	if err != nil {
		LOG.Errorf("[roate log] open dir %s failed", be.FilePath)
		return err
	}

	files, _ := dir.Readdir(-1)
	lastIndex := 0
	for _, fd := range files {
		if fd.IsDir() {
			continue
		}
		if strings.HasPrefix(fd.Name(), filePrefix) {
			tail := fd.Name()[len(filePrefix):]
			if tail != "" {
				if val, err := strconv.Atoi(tail); err == nil && val > lastIndex {
					lastIndex = val
				}
			}
		}
	}

	//Move and rotate
	lastIndex += 1
	renameFile := fmt.Sprintf("%s/%s.%s.%d", be.FilePath, be.FileName, logFiltTimeTail, lastIndex)
	curFile := fmt.Sprintf("%s/%s", be.FilePath, be.FileName)

	if oldfd, ok := be.Logrus.Out.(*os.File); ok {
		be.Locker.Lock()
		defer be.Locker.Unlock()

		if err := oldfd.Close(); err != nil {
			LOG.Errorf("[roate log] close old file %s failed", curFile)
			return err
		}

		if err := os.Rename(curFile, renameFile); err != nil {
			LOG.Errorf("[roate log] rename %s to %s failed", curFile, renameFile)
			return err
		}

		fdInfo, err := os.OpenFile(curFile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0755)
		if err != nil {
			LOG.Errorf("[roate log] open new file %s failed", curFile)
			return err
		}

		be.Logrus.Out = fdInfo
	}
	return nil
}

func (be *LogBackend) doRotate(ctx context.Context) {
	ticker := time.NewTicker(time.Second * DefaultCheckFileClicker)

	for {
		select {
		case <-ticker.C:
			rotateTime := be.checkTimeInterval()
			rotateSize := be.checkFileSize()
			if rotateTime || rotateSize {
				be.moveFile(rotateTime)
			}

		case <-ctx.Done():
			return
		}
	}
}

type Log struct {
	LogType          string
	Backends         []*LogBackend
	errLogFileWriter *os.File
}

type ConfigurationHook interface {
	// 此HOOK用于自定义日志类型
	// 重写ConfigureLogger用于覆盖默认的配置.
	ConfigureLoggerBackends(logger *Log)
}

type DefaultConfigurationHook struct {
	logFileInfo *os.File
	logFileErr  *os.File
	level       logrus.Level
}

func NewDefaultConfigurationHook(felePath, infoFile, errFile string) *DefaultConfigurationHook {
	fdInfo, err := os.OpenFile(fmt.Sprintf("%s/%s", felePath, infoFile),
		os.O_RDWR|os.O_CREATE|os.O_APPEND, 0755)
	if err != nil {
		fmt.Printf("log open info file error:%s", err)
		fdInfo = os.Stdout
	}

	fdErr, err := os.OpenFile(fmt.Sprintf("%s/%s", felePath, errFile),
		os.O_RDWR|os.O_CREATE|os.O_APPEND, 0755)
	if err != nil {
		fmt.Printf("log open error file error:%s", err)
		fdErr = os.Stderr
	}

	return &DefaultConfigurationHook{
		logFileInfo: fdInfo,
		logFileErr:  fdErr,
	}
}

var logEntryHook = LogEntryHook{}

type LogEntryHook struct {
	callerInitOnce sync.Once
	currentPackage string
}

func (hk *LogEntryHook) Levels() []logrus.Level {
	return []logrus.Level{
		logrus.FatalLevel,
		logrus.ErrorLevel,
		logrus.WarnLevel,
		logrus.InfoLevel,
		logrus.DebugLevel,
		logrus.TraceLevel,
	}
}
func (hk *LogEntryHook) Fire(entry *logrus.Entry) error {
	pcs := make([]uintptr, 25)
	depth := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:depth])

	hk.callerInitOnce.Do(func() {
		hk.currentPackage = "gitlab.msxf.com/icloud/cloud-common-go/core/xlog" //只要不移动这个类，这个路径不会变化，写死即可
	})

	var firstOcurr bool
	for f, again := frames.Next(); again; f, again = frames.Next() {
		pkg := hk.getPackageName(f.Function)
		if pkg == hk.currentPackage {
			firstOcurr = true
			continue
		} else {
			if firstOcurr {
				entry.Data["true_caller_fram"] = &f
				break
			}
		}
	}
	return nil
}

func (hk *LogEntryHook) getPackageName(f string) string {
	for {
		lastPeriod := strings.LastIndex(f, ".")
		lastSlash := strings.LastIndex(f, "/")
		if lastPeriod > lastSlash {
			f = f[:lastPeriod]
		} else {
			break
		}
	}

	return f
}

func (config *DefaultConfigurationHook) ConfigureLoggerBackends(log *Log) {
	if log.LogType == LogTypeDefault {
		backInfo := LogBackend{Logrus: logrus.New()}
		backInfo.Logrus.Level = logrus.InfoLevel
		backInfo.Logrus.ReportCaller = true
		backInfo.Logrus.Formatter = &DefaultFormatter{}
		backInfo.Logrus.Out = config.logFileInfo
		backInfo.Logrus.Hooks = logrus.LevelHooks{}
		backInfo.Logrus.Hooks.Add(&logEntryHook)

		backInfo.InitRotateAfterInitLogrus()

		backError := LogBackend{Logrus: logrus.New()}
		backError.Logrus.Level = logrus.ErrorLevel
		backError.Logrus.ReportCaller = true
		backError.Logrus.Formatter = &DefaultFormatter{}
		backError.Logrus.Out = config.logFileErr
		backError.Logrus.Hooks = logrus.LevelHooks{}
		backError.Logrus.Hooks.Add(&logEntryHook)

		backError.InitRotateAfterInitLogrus()

		backStd := LogBackend{Logrus: logrus.New()}
		backStd.Logrus.Level = logrus.InfoLevel
		backStd.Logrus.ReportCaller = true
		backStd.Logrus.Formatter = &DefaultFormatter{}
		backStd.Logrus.Out = os.Stdout
		backStd.Logrus.Hooks = logrus.LevelHooks{}
		backStd.Logrus.Hooks.Add(&logEntryHook)

		log.Backends = []*LogBackend{&backInfo, &backError, &backStd}

		log.errLogFileWriter = config.logFileErr
	}
}

type DevDefaultConfigurationHook struct {
}

func (config *DevDefaultConfigurationHook) ConfigureLoggerBackends(log *Log) {
	if log.LogType == LogTypeDefault {
		backInfo := LogBackend{Logrus: logrus.New()}

		backInfo.Logrus.Level = logrus.DebugLevel
		backInfo.Logrus.ReportCaller = false
		backInfo.Logrus.Formatter = &DefaultFormatter{}
		backInfo.Logrus.Out = os.Stdout
		backInfo.Logrus.Hooks = logrus.LevelHooks{}
		backInfo.Logrus.Hooks.Add(&logEntryHook)
		log.Backends = []*LogBackend{&backInfo}
	}
}

var configHook ConfigurationHook
var loggers = make(map[string]*Log)

func SetConfigurationHook(hook ConfigurationHook) {
	configHook = hook
	for _, l := range loggers {
		hook.ConfigureLoggerBackends(l)
	}
}

// GetLogger返回一个已有的指定类型的 logger 实例或创建一个新的 logger 实例
func getLogger(logType string) *Log {
	if l, exists := loggers[logType]; exists {
		return l
	}
	l := newLogger(logType)
	loggers[logType] = l
	return l
}

// 全局 logger 实例
func Logger() *Log {
	return getLogger(LogTypeDefault)
}

func newLogger(logType string) *Log {
	log := &Log{LogType: logType}
	if configHook == nil {
		backEnd := LogBackend{
			Logrus: logrus.New(),
		}
		backEnd.Logrus.Out = os.Stderr
		backEnd.Logrus.Formatter = &DefaultFormatter{}

		backEnd.Logrus.Hooks = make(logrus.LevelHooks)
		backEnd.Logrus.Level = logrus.InfoLevel
		backEnd.Logrus.AddHook(&logEntryHook)
		backEnd.Logrus.ReportCaller = true

		log.Backends = append(log.Backends, &backEnd)
	} else {
		configHook.ConfigureLoggerBackends(log)
	}
	return log
}

func (log *Log) Logf(level logrus.Level, format string, args ...interface{}) {
	for _, back := range log.Backends {
		if back.Logrus.Level >= level {
			back.Locker.RLock()
			defer back.Locker.RUnlock()

			back.Logrus.Logf(level, format, args...)
		}
	}
}
func (log *Log) Logln(level logrus.Level, args ...interface{}) {
	for _, back := range log.Backends {
		if back.Logrus.Level >= level {
			back.Locker.RLock()
			defer back.Locker.RUnlock()

			back.Logrus.Logln(level, args...)
		}
	}
}

func (log *Log) Log(level logrus.Level, args ...interface{}) {
	for _, back := range log.Backends {
		if back.Logrus.Level >= level {
			back.Locker.RLock()
			defer back.Locker.RUnlock()

			back.Logrus.Log(level, args...)
		}
	}
}

func (log *Log) Tracef(format string, args ...interface{}) {
	log.Logf(logrus.TraceLevel, format, args...)
}

func (log *Log) Debugf(format string, args ...interface{}) {
	log.Logf(logrus.DebugLevel, format, args...)
}

func (log *Log) Infof(format string, args ...interface{}) {
	log.Logf(logrus.InfoLevel, format, args...)
}

func (log *Log) Warnf(format string, args ...interface{}) {
	log.Logf(logrus.WarnLevel, format, args...)
}

func (log *Log) Warningf(format string, args ...interface{}) {
	log.Warnf(format, args...)
}

func (log *Log) Errorf(format string, args ...interface{}) {
	log.Logf(logrus.ErrorLevel, format, args...)
}

func (log *Log) Fatalf(format string, args ...interface{}) {
	log.Logf(logrus.FatalLevel, format, args...)
	os.Exit(1)
}

func (log *Log) Panicf(format string, args ...interface{}) {
	log.Logf(logrus.PanicLevel, format, args...)
}

func (log *Log) Trace(args ...interface{}) {
	log.Log(logrus.TraceLevel, args...)
}

func (log *Log) Debug(args ...interface{}) {
	log.Log(logrus.DebugLevel, args...)
}

func (log *Log) Info(args ...interface{}) {
	log.Log(logrus.InfoLevel, args...)
}

func (log *Log) Warn(args ...interface{}) {
	log.Log(logrus.WarnLevel, args...)
}

func (log *Log) Warning(args ...interface{}) {
	log.Warn(args...)
}

func (log *Log) Error(args ...interface{}) {
	log.Log(logrus.ErrorLevel, args...)
}

func (log *Log) Fatal(args ...interface{}) {
	log.Log(logrus.FatalLevel, args...)
	os.Exit(1)
}

func (log *Log) Panic(args ...interface{}) {
	log.Log(logrus.PanicLevel, args...)
}

func (log *Log) Traceln(args ...interface{}) {
	log.Logln(logrus.TraceLevel, args...)
}

func (log *Log) Debugln(args ...interface{}) {
	log.Logln(logrus.DebugLevel, args...)
}

func (log *Log) Infoln(args ...interface{}) {
	log.Logln(logrus.InfoLevel, args...)
}

func (log *Log) Warnln(args ...interface{}) {
	log.Logln(logrus.WarnLevel, args...)
}

func (log *Log) Warningln(args ...interface{}) {
	log.Warnln(args...)
}

func (log *Log) Errorln(args ...interface{}) {
	log.Logln(logrus.ErrorLevel, args...)
}

func (log *Log) Fatalln(args ...interface{}) {
	log.Logln(logrus.FatalLevel, args...)
	os.Exit(1)
}

func (log *Log) Panicln(args ...interface{}) {
	log.Logln(logrus.PanicLevel, args...)
}

func (log *Log) WithField(key string, value interface{}) *MsEntry {
	msentry := &MsEntry{}
	for _, back := range log.Backends {
		msentry.AddEntry(back.Logrus.WithField(key, value), &back.Locker)
	}
	return msentry
}

func (log *Log) WithFields(fields logrus.Fields) *MsEntry {
	msentry := &MsEntry{}
	for _, back := range log.Backends {
		msentry.AddEntry(back.Logrus.WithFields(fields), &back.Locker)
	}
	return msentry
}

///////////////////////////////////////
func Tracef(format string, args ...interface{}) {
	Logger().Tracef(format, args...)
}

func Debugf(format string, args ...interface{}) {
	Logger().Debugf(format, args...)
}

func Infof(format string, args ...interface{}) {
	Logger().Infof(format, args...)
}

func Warnf(format string, args ...interface{}) {
	Logger().Warnf(format, args...)
}

func Warningf(format string, args ...interface{}) {
	Logger().Warningf(format, args...)
}

func Errorf(format string, args ...interface{}) {
	Logger().Errorf(format, args...)
}

func Fatalf(format string, args ...interface{}) {
	Logger().Fatalf(format, args...)
}

func Panicf(format string, args ...interface{}) {
	Logger().Panicf(format, args...)
}

func Trace(args ...interface{}) {
	Logger().Trace(args...)
}

func Debug(args ...interface{}) {
	Logger().Debug(args...)
}

func Info(args ...interface{}) {
	Logger().Info(args...)
}

func Warn(args ...interface{}) {
	Logger().Warn(args...)
}

func Warning(args ...interface{}) {
	Logger().Warning(args...)
}

func Error(args ...interface{}) {
	Logger().Error(args...)
}

func Fatal(args ...interface{}) {
	Logger().Fatal(args...)
}

func Panic(args ...interface{}) {
	Logger().Panic(args...)
}

func Traceln(args ...interface{}) {
	Logger().Traceln(args...)
}

func Debugln(args ...interface{}) {
	Logger().Debugln(args...)
}

func Infoln(args ...interface{}) {
	Logger().Infoln(args...)
}

func Warnln(args ...interface{}) {
	Logger().Warnln(args...)
}

func Warningln(args ...interface{}) {
	Logger().Warningln(args...)
}

func Errorln(args ...interface{}) {
	Logger().Errorln(args...)
}

func Fatalln(args ...interface{}) {
	Logger().Fatalln(args...)
}

func Panicln(args ...interface{}) {
	Logger().Panicln(args...)
}

func WithField(key string, value interface{}) *MsEntry {
	return Logger().WithField(key, value)
}

func WithContext(ctx context.Context) *MsEntry {
	return Logger().WithField("requestId", requestID(ctx))
}

func requestID(ctx context.Context) string {
	value := ctx.Value("reqId")
	if value == nil {
		return ""
	}
	reqID, ok := value.(string)
	if !ok {
		return ""
	}
	return reqID
}

func WithFields(fields logrus.Fields) *MsEntry {
	return Logger().WithFields(fields)
}
func (log *Log) GetErrLogFile() *os.File {
	return log.errLogFileWriter
}
