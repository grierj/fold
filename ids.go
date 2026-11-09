package main

import "strconv"

// thousands is n with a comma between each group of three digits.
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// idString is a row id as the text that keys, paths and JSON ids carry.
func idString(n uint) string { return strconv.FormatUint(uint64(n), 10) }
