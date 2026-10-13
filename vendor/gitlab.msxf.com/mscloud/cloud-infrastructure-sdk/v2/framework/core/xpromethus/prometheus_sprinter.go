package xpromethus

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"strings"
)

func SprintMetricInPrometheusViaWebText() string{
	var c strings.Builder
	sprint0(&c)
	return c.String()
}

func SprintMetricInPrometheus(c *gin.Context) {
	sprint0(c.Writer)
}

func SprintMetric(writer io.StringWriter) {
	sprint0(writer)
}

func sprint0(writer io.StringWriter) {
	MetricManager.metricMap.Range(func(method, value interface{}) bool {
		methodStr := method.(string)

		mds := strings.Split(methodStr, labelSep)

		metricName := ""
		labelStr := ""
		if len(mds) == 1 {
			metricName = mds[0]
		} else if len(mds) == 2 {
			metricName = mds[0]
			labelStr = mds[1]
		}

		labelSlice := make([]string, 0)
		if len(labelStr) > 0 {
			labelSlice = strings.Split(labelStr, "&")
		}

		convertedMetricName := convertSpecialChar(metricName)

		if len(labelSlice) > 0 {
			_, err := writer.WriteString("# HELP ")
			if err != nil {
				return false
			}
			_, err = writer.WriteString(convertedMetricName)
			if err != nil {
				return false
			}
			_, err = writer.WriteString(" tag:"+mds[1])
			if err != nil {
				return false
			}
			_, err = writer.WriteString("\n")
			if err != nil {
				return false
			}
		}

		_, err := writer.WriteString("# TYPE ")
		if err != nil {
			return false
		}


		_, err = writer.WriteString(convertedMetricName)
		if err != nil {
			return false
		}

		_, err = writer.WriteString(" gauge")
		if err != nil {
			return false
		}

		_, err = writer.WriteString("\n")
		if err != nil {
			return false
		}

		metric := value.(metricViewer)
		for label, value := range *metric.GetMetric() {
			_, err = writer.WriteString(convertedMetricName)
			if err != nil {
				return false
			}

			_, err = writer.WriteString(labelString(label, labelSlice))
			if err != nil {
				return false
			}

			_, err = writer.WriteString(" " + fmt.Sprintf("%.5f", value))
			if err != nil {
				return false
			}

			_, err = writer.WriteString("\n")
			if err != nil {
				return false
			}
		}
		return true
	})
}

func labelString(label string, ls []string) string {
	if len(ls) == 0 {
		return "{metric=\"" + label + "\"}"
	}

	labelString := ""
	for _, v := range ls {
		vs := strings.Split(v, "=")
		labelString += ","+vs[0]+"=\""+vs[1] + "\""
	}
	return fmt.Sprintf("{metric=\"" + label + "\"%s}", labelString)
}

func convertSpecialChar(str string) string {
	var encodeStr strings.Builder

	for _, c := range str {
		if isSpecialChar(byte(c)) {
			encodeStr.WriteByte('_')
		} else {
			encodeStr.WriteByte(byte(c))
		}
	}

	return encodeStr.String()
}

func isSpecialChar(c byte) bool {
	return !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c == '_') || (c >= '0' && c <= '9'))
}
