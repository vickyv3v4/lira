package xutils

import (
	"fmt"
	"github.com/golang-jwt/jwt"
	"github.com/spf13/cast"
)

const priKey = "!GCLOUD@VERITY#$%"

type UserToken struct {
	CloudAccountId string // 主账号ID
	RamAccountId   string // ram角色账号ID
	TenantId       string // 租户ID
	ProjectName    string // 项目名
	ExpireAt       int64  // 过期时间：秒级时间戳，1666840014
}

func DecodeUserToken(tokenString string) (*UserToken, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("signing method err: %v", token.Header["alg"])
		}
		return []byte(priKey), nil
	})

	ut := new(UserToken)
	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		ut.CloudAccountId = cast.ToString(claims["cloudAccountId"])
		ut.RamAccountId = cast.ToString(claims["ramAccountId"])
		ut.TenantId = cast.ToString(claims["tenantId"])
		ut.ProjectName = cast.ToString(claims["projectName"])
		ut.ExpireAt = cast.ToInt64(claims["exp"])

		return ut, nil
	}
	return nil, err
}
