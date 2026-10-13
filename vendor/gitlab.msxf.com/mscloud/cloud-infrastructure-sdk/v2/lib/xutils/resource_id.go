package xutils

import (
	"math/rand"
	"strings"
	"time"
)

const (
	resourceIDSeed       = "abcdefghijkmnopqrstuvwxyz023456789"
	resourceIDSeedLength = len(resourceIDSeed)
	resourceIDSeedPrefix = "resourceGroup-"
	resourceIDLength     = len(resourceIDSeedPrefix) + 18
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

// ResourceID 生成一串长度为32，以 resourceGroup- 为前缀的随机字符串id
func ResourceID() string {
	stringBuilder := strings.Builder{}
	stringBuilder.Grow(resourceIDLength) // 形如 resourceGroup-7miyc2t4jcc07w9f58, 长度为32
	stringBuilder.WriteString(resourceIDSeedPrefix)

	for i := 0; i < 18; i++ {
		stringBuilder.WriteByte(resourceIDSeed[rand.Intn(resourceIDSeedLength)])
	}

	return stringBuilder.String()
}
