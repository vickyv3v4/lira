package xutils

import (
	"encoding/hex"
	"github.com/tjfoc/gmsm/sm4"
)
/**
	SM4 解密
 */

func Decrypt(key []byte, data string) ([]byte, error) {
	cipherText, err := hex.DecodeString(data)
	if err != nil {
		return nil, err
	}
	cipher, err := sm4.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plaintext := make([]byte, len(cipherText))
	for i := 0; i < len(cipherText); i += cipher.BlockSize() {
		cipher.Decrypt(plaintext[i:i+cipher.BlockSize()], cipherText[i:i+cipher.BlockSize()])
	}
	plaintext = unPadding(plaintext)
	return plaintext, nil
}

func unPadding(src []byte) []byte {
	l := len(src)
	pad := int(src[l-1])
	return src[:l-pad]
}