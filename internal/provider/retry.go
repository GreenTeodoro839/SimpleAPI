package provider

// IsRetryableStatus reports whether an HTTP status should be treated as an
// upstream failure for failover purposes.
func IsRetryableStatus(code int, retryCodes []int) bool {
	for _, rc := range retryCodes {
		if code == rc {
			return true
		}
	}
	return false
}
