package xserializer

import (
	"fmt"
	"github.com/pkg/errors"
	"io"
	"os"
	"path/filepath"
)

// 支持类型 JSON 或者 YAML
type EncoderType int

const (
	// JSON
	JSON EncoderType = 1 + iota
	// YAML
	YAML
)

type Encoder interface {
	MarshalIndent(v interface{}) (string, error)

	Marshal(v interface{}) (string, error)

	UnMarshal(r io.Reader, v interface{}) error

	UnMarshalFile(f io.Reader, v interface{}) error

	UnMarshalStr(data string, result interface{}) error
}

func NewEncoder(encoder EncoderType) (Encoder, error) {
	switch encoder {
	case JSON:
		return newJSONEncoder(), nil
	case YAML:
		return newYAMLEncoder(), nil
	default:
		return nil, errors.New(fmt.Sprintf("not support type: %d", encoder))
	}
}

func NewJsonEncoder() Encoder {
	return newJSONEncoder()
}

func NewYamlEncoder() Encoder {
	return newYAMLEncoder()
}

// 通过文件后缀实例化encoder
func NewEncoderFromFileExt(filename string) (Encoder, error) {

	if et, err := EncoderTypeFromExt(filename); err != nil {
		return nil, err
	} else {
		return NewEncoder(et)
	}
}

// 通过文件后缀返回 encoder 类型
func EncoderTypeFromExt(filename string) (EncoderType, error) {
	switch filepath.Ext(filename) {
	case ".yml", ".yaml":
		return YAML, nil
	case ".json":
		return JSON, nil
	}
	return JSON, errors.New("not support extension")
}

// ConvertFile JSON 文件与 YAML 文件相互转换
func ConvertFile(infile, outfile string, dataType interface{}) error {
	var fromEnc, toEnc Encoder
	var encErr error

	if fromEnc, encErr = NewEncoderFromFileExt(infile); encErr != nil {
		return encErr
	}

	if toEnc, encErr = NewEncoderFromFileExt(outfile); encErr != nil {
		return encErr
	}

	file, err := os.Open(infile)
	if err != nil {
		return err
	}

	if err := fromEnc.UnMarshal(file, dataType); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outfile), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(outfile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if data, err := toEnc.MarshalIndent(dataType); err != nil {
		return err
	} else {
		f.WriteString(data)
	}
	return nil
}
