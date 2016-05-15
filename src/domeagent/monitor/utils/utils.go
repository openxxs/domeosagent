package utils

import "fmt"

// Returns a mask of all cores on the machine if the passed-in mask is empty.
func FixCpuMask(mask string, cores int) string {
	if mask == "" {
		if cores > 1 {
			mask = fmt.Sprintf("0-%d", cores-1)
		} else {
			mask = "0"
		}
	}
	return mask
}