package utils

import "time"

// durationSeconds converts a whole-second count into a time.Duration.
func durationSeconds(s int) time.Duration {
	return time.Duration(s) * time.Second
}

// Seconds is the exported form of durationSeconds for callers outside utils.
func Seconds(s int) time.Duration {
	return durationSeconds(s)
}
