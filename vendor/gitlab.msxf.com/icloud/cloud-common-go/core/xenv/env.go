package xenv

import "os"

func env(k string) string {
	return os.Getenv(k)
}

func Product() bool {
	return env("env_cd") == "online"
}

func Local() bool {
	return env("env_cd") == ""
}

func Test() bool {
	e := env("env_cd")
	return e != "" && e != "online"
}
