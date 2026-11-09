package main

import "strconv"

// idString is a row id as the text that keys, paths and JSON ids carry.
func idString(n uint) string { return strconv.FormatUint(uint64(n), 10) }
