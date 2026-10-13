/**
* @Author: wei.peng03
* @Date: 2022/7/20
 */

package xcontext

type CloudResponse struct {
	RequestId string `json:"requestId"`
	Code int `json:"code"`
	Msg string `json:"msg"`
	Result interface{} `json:"result"`
}
