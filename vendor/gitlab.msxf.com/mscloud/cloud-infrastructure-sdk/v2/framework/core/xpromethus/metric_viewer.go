/**
* @Author: wei.peng03
* @Date: 2021/12/17
 */

package xpromethus

type metricViewer interface{
	GetMetric() *metricData
}
