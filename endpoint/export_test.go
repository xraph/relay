package endpoint

// SetUnsignedPageSizeForTest shrinks ListUnsigned's page size so a test can
// exercise paging with a few endpoints. It returns a func that restores it.
func SetUnsignedPageSizeForTest(n int) (restore func()) {
	prev := unsignedPageSize
	unsignedPageSize = n
	return func() { unsignedPageSize = prev }
}
