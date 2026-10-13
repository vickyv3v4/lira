package xserializer

import (
	"github.com/ghodss/yaml"
	"io"
	"io/ioutil"
	"strings"
)

type YAMLEncoder struct{}

func newYAMLEncoder() *YAMLEncoder {
	return &YAMLEncoder{}
}

func (e *YAMLEncoder) MarshalIndent(data interface{}) (string, error) {
	return e.Marshal(data)
}

func (e *YAMLEncoder) Marshal(data interface{}) (string, error) {

	if response, err := yaml.Marshal(data); err != nil {
		return "", err
	} else {
		return string(response), err
	}
}

func (e *YAMLEncoder) UnMarshalFile(f io.Reader, v interface{}) error {
	b, err := ioutil.ReadAll(f)
	if err != nil {
		return err
	}

	return yaml.Unmarshal(b, v)
}

func (e *YAMLEncoder) UnMarshal(r io.Reader, result interface{}) error {
	b, err := ioutil.ReadAll(r)
	if err != nil {
		return err
	}

	if err := yaml.Unmarshal(b, result); err != nil {
		return err
	}
	return nil
}

func (e *YAMLEncoder) UnMarshalStr(data string, result interface{}) error {
	return e.UnMarshal(strings.NewReader(data), result)
}
