package xlog

import (
	"bytes"
	"fmt"
	"github.com/sirupsen/logrus"
	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/framework/core/utils/xcolor"
	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/framework/core/xenv"
	"os"
	"path"
	"runtime"
	"strings"
)

const (
	defaultTimestampFormat = "2006-01-02 15:04:05.000"
)

type fieldKey string

// FieldMap allows customization of the key names for default fields.
type FieldMap map[fieldKey]string

func (f FieldMap) resolve(key fieldKey) string {
	if k, ok := f[key]; ok {
		return k
	}

	return string(key)
}

type DefaultFormatter struct {
}

func (f *DefaultFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	var b *bytes.Buffer
	if entry.Buffer != nil {
		b = entry.Buffer
	} else {
		b = &bytes.Buffer{}
	}

	valTime := entry.Time.Format(defaultTimestampFormat)
	lev, _ := entry.Level.MarshalText()
	valLevel := strings.ToUpper(string(lev))
	valMsg := entry.Message
	if xenv.Debug() {
		var colorize = xcolor.Red
		valMsg = colorize(valMsg)
		if entry.Level >= logrus.DebugLevel {
			colorize = xcolor.Green
		} else if entry.Level >= logrus.InfoLevel {
			colorize = xcolor.Blue
		} else if entry.Level >= logrus.WarnLevel {
			colorize = xcolor.Yellow
		} else {
			colorize = xcolor.Red
		}
		valLevel = colorize(valLevel)
	}

	var valCaller string
	if val, ok := entry.Data["true_caller_fram"]; ok && val != nil {
		caller, _ := val.(*runtime.Frame)
		if xenv.Debug() {
			valCaller = fmt.Sprintf(" %s:%d ", caller.File, caller.Line)
		} else {
			var val_func string
			if idx := strings.LastIndex(caller.Function, "."); idx >= 0 {
				val_func = caller.Function[idx+1:]
			}
			valCaller = fmt.Sprintf("%s-%s:%d", path.Base(caller.File), val_func, caller.Line)
		}

		delete(entry.Data, "true_caller_fram")
	}

	//2023-02-13 15:35:13.194  INFO 1 --- [http-nio-8080-exec-36] [.c.c.u.UserTransPasswordCacheableService:65] : [TxId : apigateway-ltG-15^1676210311419^4890427 , SpanId : 525502106696189323] 从redis获取是否设置交易密码,uniqueId=[601ebcc85657444eaa65ae530384566c], status=[true]
	entryBuf := new(bytes.Buffer)

	entryLen := len(entry.Data)
	i := 0
	if entryLen > 0 {
		for k, v := range entry.Data {
			stringVal, ok := v.(string)
			if !ok {
				stringVal = fmt.Sprint(v)
			}

			if i+1 == entryLen {
				entryBuf.WriteString(k + ":" + stringVal)
			} else {
				entryBuf.WriteString(k + ":" + stringVal + ",")
			}
			i++
		}
	}

	b.WriteString(fmt.Sprintf("%s %s %d --- [goroutine-n] [%s] : [%s] %s", valTime, valLevel, getPID(), valCaller, entryBuf.String(), valMsg))

	b.WriteByte('\n')
	return b.Bytes(), nil
}

var pid int

func getPID() int {
	if pid > 0 {
		return pid
	}
	return os.Getpid()
}
