package xhttp

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"gitlab.msxf.com/icloud/cloud-common-go/core/xerrors"
	"gitlab.msxf.com/icloud/cloud-common-go/core/xlog"
	"net/http"
)

type (
	Rsp struct {
		Code      int         `json:"code"`
		Msg       string      `json:"msg"`
		Data      interface{} `json:"data"`
		RequestID string      `json:"requestId"`
	}

	Api       func(c *gin.Context) (interface{}, error)
	StreamApi func(c *gin.Context) ([]byte, string, error)
)

func WrapApi(api Api) gin.HandlerFunc {
	return func(c *gin.Context) {
		data, err := api(c)
		if err != nil {
			xlog.Errorf("sever err [%+v]", err)
			var xerr *xerrors.Error
			if ok := errors.As(err, &xerr); ok {
				c.JSON(http.StatusOK, &Rsp{
					Code:      xerr.Code,
					Msg:       xerr.Msg,
					Data:      nil,
					RequestID: RequestID(c.Request.Context()),
				})
				c.Next()
				return
			}

			c.JSON(http.StatusOK, &Rsp{
				Code:      xerrors.InternalError,
				Msg:       err.Error(),
				Data:      nil,
				RequestID: RequestID(c.Request.Context()),
			})
			c.Next()
			return
		}

		c.JSON(http.StatusOK, &Rsp{
			Code:      0,
			Msg:       "",
			Data:      data,
			RequestID: RequestID(c.Request.Context()),
		})

	}
}

func WrapStreamApi(api StreamApi) gin.HandlerFunc {
	return func(c *gin.Context) {
		stream, contentType, err := api(c)
		if err != nil {
			xlog.Errorf("sever err [%+v]", err)
			var xerr *xerrors.Error
			if ok := errors.As(err, &xerr); ok {
				c.JSON(http.StatusOK, &Rsp{
					Code:      xerr.Code,
					Msg:       xerr.Msg,
					Data:      nil,
					RequestID: RequestID(c.Request.Context()),
				})
				c.Next()
				return
			}

			c.JSON(http.StatusOK, &Rsp{
				Code:      xerrors.InternalError,
				Msg:       err.Error(),
				Data:      nil,
				RequestID: RequestID(c.Request.Context()),
			})
			c.Next()
			return
		}

		c.Data(http.StatusOK, contentType, stream)
	}
}

func RequestID(ctx context.Context) string {
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
