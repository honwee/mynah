package instinct

import "strconv"

func fmtFloat(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }
