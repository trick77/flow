package sessions

import (
	"context"

	"github.com/trick77/flow/internal/metering"
)

// The follower feeds the recorder, and sessions records started-session waits and pull-request changes through
// it: metering imports no area, so this compiles.
var (
	_ Meter                                             = (*metering.Recorder)(nil)
	_ func(context.Context, metering.PullRequest) error = (*metering.Recorder)(nil).RecordPullRequest
	_ func(context.Context, metering.Wait) error        = (*metering.Recorder)(nil).RecordWait
)
