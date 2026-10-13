package xcolor

import "fmt"

func Red(msg string) string {
	return fmt.Sprintf("\x1b[31m%s\x1b[0m", msg)
}

func Yellow(msg string) string {
	return fmt.Sprintf("\x1b[33m%s\x1b[0m", msg)
}

func Blue(msg string) string {
	return fmt.Sprintf("\x1b[34m%s\x1b[0m", msg)
}

func Green(msg string) string {
	return fmt.Sprintf("\x1b[32m%s\x1b[0m", msg)
}
