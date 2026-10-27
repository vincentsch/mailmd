package writer_test

import "errors"

// Windows has no named pipes in the file system.
func makeFIFO(string) error { return errors.ErrUnsupported }
