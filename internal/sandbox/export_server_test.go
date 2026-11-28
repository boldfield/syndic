package sandbox

import "io"

// ServerHelper exposes the body of the "serve" helper mode to tests.
func ServerHelper(stdin io.Reader, stdout io.Writer) int { return serverHelper(stdin, stdout) }
