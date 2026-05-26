package transcript

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

// scannerBufMax must exceed the largest tool_result payload Claude persists.
// ccdiag uses 10MB; tool results occasionally hit a few MB.
const scannerBufMax = 10 * 1024 * 1024

// ParseFile reads every line of a JSONL transcript. Malformed lines are
// silently skipped (callers may want to log; for state classification we
// just need the recent good ones).
func ParseFile(path string) ([]Line, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseReader(f)
}

func parseReader(r io.Reader) ([]Line, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), scannerBufMax)
	var out []Line
	for sc.Scan() {
		b := sc.Bytes()
		if len(b) == 0 {
			continue
		}
		var ln Line
		if err := json.Unmarshal(b, &ln); err != nil {
			continue
		}
		out = append(out, ln)
	}
	return out, sc.Err()
}

// ParseAppended reads from offset to EOF, returning new lines and the new
// offset. Used by the tailer to incrementally consume appended JSONL.
//
// If the file shrank below offset (truncate/rotate), we reread from 0.
func ParseAppended(path string, offset int64) (lines []Line, newOffset int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	start := offset
	if fi.Size() < offset {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, offset, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), scannerBufMax)
	for sc.Scan() {
		b := sc.Bytes()
		if len(b) == 0 {
			continue
		}
		var ln Line
		if err := json.Unmarshal(b, &ln); err != nil {
			continue
		}
		lines = append(lines, ln)
	}
	// Scanner may have read partial trailing line; report position via Stat
	// rather than scanner internals. Re-stat to capture appends during scan.
	if fi2, e := os.Stat(path); e == nil {
		newOffset = fi2.Size()
	} else {
		newOffset = start
	}
	return lines, newOffset, sc.Err()
}
