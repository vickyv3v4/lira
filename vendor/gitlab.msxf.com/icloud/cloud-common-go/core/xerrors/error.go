package xerrors

import (
	"fmt"
	"github.com/pkg/errors"
)

const (
	Success                     = 0
	CommonIllegalArgError       = -11000000
	CommonTokenExpiredError     = -11001000
	CommonPermissionDeniedError = -11002000
	CommonFreqLimitedError      = -11003000
	CommonResourceNotFoundError = -11005000
	CommonRequestArgFormatError = -11006000
	CommonValueOutOfRangeError  = -11007000
	CommonTokenInvalidError     = -11008000
	CommonUnknownServiceError   = -11009000
	CommonRequestExpiredError   = -11011000
	InternalError               = -11102000
	InternalServerBusyError     = -11103000
	InternalServerTimeoutError  = -11104000
	InternalDbMysqlError        = -11105000
	InternalDbRedisError        = -11106000
	InternalDbMongoError        = -11107000
	InternalStorageError        = -11108000
	InternalMsgPushError        = -11109000
	InternalMqKafkaConsumeError = -11110000
	InternalMqKafkaProduceError = -11111000
	InternalElasticSearchError  = -11112000
	InternalErrorTimeout        = -11113000
)

var Msg = map[int]string{
	Success:                     "请求成功",
	CommonIllegalArgError:       "参数不合法",
	CommonTokenExpiredError:     "token已过期",
	CommonPermissionDeniedError: "用户无权限",
	CommonFreqLimitedError:      "请求频率过高",
	CommonResourceNotFoundError: "请求资源不存在",
	CommonRequestArgFormatError: "请求参数格式错误",
	CommonValueOutOfRangeError:  "数值超出范围",
	CommonTokenInvalidError:     "token 无效",
	CommonUnknownServiceError:   "unknown service",
	CommonRequestExpiredError:   "请求已过期",
	InternalError:               "服务内部错误",
	InternalServerBusyError:     "服务器繁忙",
	InternalServerTimeoutError:  "服务器超时",
	InternalDbMysqlError:        "MySQL错误",
	InternalDbRedisError:        "Redis错误",
	InternalDbMongoError:        "MongoDB错误",
	InternalStorageError:        "存储错误",
	InternalMsgPushError:        "消息推送错误",
	InternalMqKafkaConsumeError: "kafka消费错误",
	InternalMqKafkaProduceError: "kafka发送错误",
	InternalElasticSearchError:  "Elasticsearch错误",
	InternalErrorTimeout:        "接口请求超时",
}

type Error struct {
	Code   int
	Msg    string
	Reason string
	cause  error
}

func (e *Error) Error() string {
	return fmt.Sprintf("error: code = %d reason = %s message = %s cause = %+v", e.Code, e.Reason, e.Msg, e.cause)
}

func (e *Error) Unwrap() error {
	return e.cause
}

func NewAssignCodeError(code int) error {
	msg, ok := Msg[code]
	if !ok {
		msg = "其他错误"
	}
	return errors.WithStack(&Error{
		Code: code,
		Msg:  msg,
	})
}

func NewAssignCodeAndCauseError(code int, err error) error {
	msg, ok := Msg[code]
	if !ok {
		msg = "其他错误"
	}
	return errors.WithStack(&Error{
		Code:  code,
		Msg:   msg,
		cause: err,
	})
}

func NewCustomError(code int, format string, a ...interface{}) error {
	return errors.WithStack(&Error{
		Code: code,
		Msg:  fmt.Sprintf(format, a...),
	})
}

func NewCustomAndCauseError(code int, err error, format string, a ...interface{}) error {
	return errors.WithStack(&Error{
		Code:  code,
		Msg:   fmt.Sprintf(format, a...),
		cause: err,
	})
}
