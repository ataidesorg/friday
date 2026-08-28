package observability

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/ataidesorg/friday/internal/core"
)

// maxLine bounds one trail line; an event is small, so a longer line is
// corruption, not data.
const maxLine = 16 << 20

// ReadTrail decodes every line strictly and stops at the first malformed
// one, naming its line number. A missing file is core.ErrNotFound.
func ReadTrail(path string) ([]core.Event, error) {
	f, err := os.Open(path) //nolint:gosec // caller-supplied trail path
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: trail %s", core.ErrNotFound, path)
	}
	if err != nil {
		return nil, fmt.Errorf("open trail: %w", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	var events []core.Event
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e core.Event
		if err := e.UnmarshalJSON(line); err != nil {
			return events, fmt.Errorf("trail %s line %d: %w", path, n, err)
		}
		events = append(events, e)
	}
	if err := sc.Err(); err != nil {
		return events, fmt.Errorf("read trail %s: %w", path, err)
	}
	return events, nil
}
