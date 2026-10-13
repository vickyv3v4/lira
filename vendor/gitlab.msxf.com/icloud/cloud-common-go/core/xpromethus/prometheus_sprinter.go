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

func sprint0(writer io.StringWriter) {
	MetricManager.metricMap.Range(func(method, value interface{}) bool {
		_, err := writer.WriteString("# TYPE ")
		if err != nil {
			return false
		}

		_, err = writer.WriteString(convertSpecialChar(method.(string)))
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
			_, err = writer.WriteString(convertSpecialChar(method.(string)))
			if err != nil {
				return false
			}

			_, err = writer.WriteString(labelString(label))
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

func labelString(label string) string {
	return "{metric=\"" + label + "\"}"
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
