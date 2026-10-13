package xenv

import "os"

func Get(k string) string {
	return env(k)
}

func env(k string) string {
	return os.Getenv(k)
}

func Product() bool {
	return env("env_cd") == "online"
}

func Local() bool {
	return env("env_cd") == ""
}

func Debug() bool {
	return env("MS_DEBUG") == "1" || env("MS_DEBUG") == "true"
}

func Test() bool {
	e := env("env_cd")
	return e != "" && e != "online"
}
