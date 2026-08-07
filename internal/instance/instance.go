// Package instance derives the dansal instance name from the config file
// path passed on the command line. Both dansal and dansal-webmin use it to
// pick their syslog tag.
package instance

import (
	"os"
	"path/filepath"
	"strings"
)

// FromConfigArg returns the instance name — the directory containing the
// config file — derived from the --config/-config command-line argument, or
// "" if no such argument is present.
func FromConfigArg() string {
	for i, arg := range os.Args {
		var path string
		switch {
		case (arg == "--config" || arg == "-config") && i+1 < len(os.Args):
			path = os.Args[i+1]
		case strings.HasPrefix(arg, "--config="):
			path = arg[len("--config="):]
		case strings.HasPrefix(arg, "-config="):
			path = arg[len("-config="):]
		}
		if path != "" {
			return filepath.Base(filepath.Dir(path))
		}
	}
	return ""
}
