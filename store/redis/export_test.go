package redis

// SetScanWindow lets tests shrink the window ListDeliveries post-filters
// over, so a small data set can exercise the incomplete-search path.
func SetScanWindow(s *Store, n int) { s.scanWindow = n }
