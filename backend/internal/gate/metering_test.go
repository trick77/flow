package gate

import (
	"context"

	"github.com/trick77/flow/internal/metering"
)

// The gate records its runs and check-queue waits in the ledger; metering imports no area, so this compiles.
var (
	_ func(context.Context, metering.CheckRun) error = (*metering.Recorder)(nil).RecordCheckRun
	_ func(context.Context, metering.Wait) error     = (*metering.Recorder)(nil).RecordWait
)
