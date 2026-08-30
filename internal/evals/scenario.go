// Package evals runs evaluation scenarios: a fixture project, a task, and
// deterministic expectations checked against the workspace and the trail
// after a run. The runner and the checks ship here; comparing baselines
// against candidates does not.
package evals

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ataidesorg/ink/internal/core"
)

// LoadScenario reads a scenario file. Unknown fields are errors, the fixture
// path resolves relative to the file and must exist, and every expectation
// must carry the fields its kind needs.
func LoadScenario(path string) (core.EvaluationScenario, error) {
	var s core.EvaluationScenario
	b, err := os.ReadFile(path) //nolint:gosec // caller-supplied scenario path
	if errors.Is(err, fs.ErrNotExist) {
		return s, fmt.Errorf("%w: scenario %s", core.ErrNotFound, path)
	}
	if err != nil {
		return s, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, fmt.Errorf("%w: %s: %w", core.ErrInvalidInput, path, err)
	}
	if !filepath.IsAbs(s.Fixture) {
		s.Fixture = filepath.Join(filepath.Dir(path), s.Fixture)
	}
	if s.Fixture, err = filepath.Abs(s.Fixture); err != nil {
		return s, fmt.Errorf("scenario %s: fixture: %w", path, err)
	}
	if err := validate(s); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func validate(s core.EvaluationScenario) error {
	switch {
	case strings.TrimSpace(string(s.ID)) == "":
		return fmt.Errorf("%w: scenario id is empty", core.ErrInvalidInput)
	case strings.TrimSpace(s.Task) == "":
		return fmt.Errorf("%w: scenario %s: task is empty", core.ErrInvalidInput, s.ID)
	case len(s.Expectations) == 0:
		return fmt.Errorf("%w: scenario %s: no expectations", core.ErrInvalidInput, s.ID)
	}
	st, err := os.Stat(s.Fixture)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w: fixture %s", core.ErrNotFound, s.Fixture)
	case err != nil:
		return err
	case !st.IsDir():
		return fmt.Errorf("%w: fixture %s is not a directory", core.ErrInvalidInput, s.Fixture)
	}
	for i, e := range s.Expectations {
		if err := validateExpectation(e); err != nil {
			return fmt.Errorf("%w: scenario %s: expectation %d: %w", core.ErrInvalidInput, s.ID, i, err)
		}
	}
	return nil
}

func validateExpectation(e core.Expectation) error {
	need := func(ok bool, field string) error {
		if !ok {
			return fmt.Errorf("%s needs %s", e.Kind, field)
		}
		return nil
	}
	switch e.Kind {
	case core.ExpectFileExists:
		return need(e.Path != "", "path")
	case core.ExpectFileContains:
		return errors.Join(need(e.Path != "", "path"), need(e.Needle != "", "needle"))
	case core.ExpectFileSHA256:
		return errors.Join(need(e.Path != "", "path"), need(len(e.SHA256) == 64, "a 64-hex sha256"))
	case core.ExpectCommandSucceeds, core.ExpectCommandFails:
		return need(len(e.Argv) > 0 && e.Argv[0] != "", "argv")
	case core.ExpectApprovalRequired, core.ExpectNoSecretLeak:
		return nil
	case core.ExpectMemoryWritten:
		return need(e.Memory != "", "memory")
	}
	return fmt.Errorf("unknown kind %q", e.Kind)
}
