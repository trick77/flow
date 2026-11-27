package claude

import (
	"errors"
	"os"
	"testing"
)

// A spawn that fails halfway closes every pipe end it opened.
func TestSpawn_closesPipesOnFailure(t *testing.T) {
	for fail := 1; fail <= 3; fail++ {
		a := New(Config{Argv: []string{"/bin/true"}, State: func() State { return State{} }})
		var opened []*os.File
		n := 0
		a.pipe = func() (*os.File, *os.File, error) {
			n++
			if n == fail {
				return nil, nil, errors.New("no pipe")
			}
			r, w, err := os.Pipe()
			opened = append(opened, r, w)
			return r, w, err
		}
		if _, err := a.spawn(); err == nil {
			t.Fatalf("pipe %d failing: spawn succeeded", fail)
		}
		for i, f := range opened {
			if err := f.Close(); !errors.Is(err, os.ErrClosed) {
				t.Errorf("pipe %d failing: end %d left open", fail, i)
			}
		}
	}
}
