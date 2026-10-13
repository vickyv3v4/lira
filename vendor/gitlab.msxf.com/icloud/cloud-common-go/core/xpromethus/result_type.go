package xpromethus

type resultType int

const (
	requestSuccess resultType = iota
	requestError
	requestPanic
)
