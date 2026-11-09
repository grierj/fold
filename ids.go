package main

import "strconv"

func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func idString(n uint) string { return strconv.FormatUint(uint64(n), 10) }
