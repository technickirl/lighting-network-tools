// Package luminex holds what the tools share: mDNS discovery of _luminex._tcp,
// device classification from the TXT record, local-subnet reachability,
// timestamped logging and, the device HTTP client.
package luminex

import (
	"fmt"
	"os"
	"time"
)

func ts() string { return time.Now().Format("15:04:05.000") }

func Logf(format string, a ...any) {
	fmt.Fprintf(os.Stdout, "[%s] %s\n", ts(), fmt.Sprintf(format, a...))
}
func Warnf(format string, a ...any) {
	fmt.Fprintf(os.Stdout, "[%s] WARN %s\n", ts(), fmt.Sprintf(format, a...))
}
func Errorf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "[%s] ERROR %s\n", ts(), fmt.Sprintf(format, a...))
}
