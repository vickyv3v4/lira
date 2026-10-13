package daemon

var syscallUmask = func(int) int {
	// nothing to do
	return 0
}
