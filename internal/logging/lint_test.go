package logging

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestNoUnleveledLogCalls keeps new code on the levelled helpers (#1646). The
// standard log.Print, log.Printf and log.Println calls would reach journald
// at info and bypass server.log_level. log.Fatal stays: it is for startup
// failures and is sent at Err.
func TestNoUnleveledLogCalls(t *testing.T) {
	re := regexp.MustCompile(`\blog\.(Print|Printf|Println)\(`)
	var hits []string
	for _, root := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(src), "\n") {
				if re.MatchString(line) {
					hits = append(hits, path+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(hits) > 0 {
		t.Fatalf("use logging.Debugf/Infof/Warnf/Errorf instead of log.Print*:\n%s", strings.Join(hits, "\n"))
	}
}
