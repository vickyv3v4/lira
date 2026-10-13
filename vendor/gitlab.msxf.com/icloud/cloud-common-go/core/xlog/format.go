package xlog

import (
	"bytes"
	"fmt"
	"github.com/sirupsen/logrus"
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
	var valCaller string
	if val, ok := entry.Data["true_caller_fram"]; ok && val != nil {
		caller, _ := val.(*runtime.Frame)
		var val_func string
		if idx := strings.LastIndex(caller.Function, "."); idx >= 0 {
			val_func = caller.Function[idx+1:]
		}
		valCaller = fmt.Sprintf("%s-%s:%d", path.Base(caller.File), val_func, caller.Line)

		delete(entry.Data, "true_caller_fram")
	}

	b.WriteString(fmt.Sprintf("%s %s [%s]: %s", valTime, valLevel, valCaller, entry.Message))

	if len(entry.Data) > 0 {
		b.WriteString(" [")
		for k, v := range entry.Data {
			stringVal, ok := v.(string)
			if !ok {
				stringVal = fmt.Sprint(v)
			}
			b.WriteString(k + "=" + stringVal + ",")

		}
		b.WriteByte(']')
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}
