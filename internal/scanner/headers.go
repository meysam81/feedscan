package scanner

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// ParseHeaders turns "Name: value" lines into an http.Header. Blank lines and
// lines starting with '#' are skipped so a headers file can carry comments.
func ParseHeaders(lines []string) (http.Header, error) {
	h := http.Header{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("invalid header %q: want \"Name: value\"", line)
		}
		h.Add(name, strings.TrimSpace(value))
	}
	return h, nil
}

// ReadHeaderFile reads a file of "Name: value" lines and returns them raw for
// ParseHeaders.
func ReadHeaderFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return lines, nil
}
