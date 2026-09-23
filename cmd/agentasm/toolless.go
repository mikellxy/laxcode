package agentasm

import (
	"context"
)

func assembleToolless(ctx context.Context, in QAInput) (*QAAssembled, error) {
	core, err := assembleCore(ctx, in.WorkDir, in.HomeDir, in.SessionID, in.Consumer, false)
	if err != nil {
		return nil, err
	}
	if err := core.service.InitSession(ctx); err != nil {
		core.cleanup()
		return nil, err
	}
	return &QAAssembled{
		Service: core.service, Session: core.session, Cleanup: core.cleanup, tracer: core.tracer,
	}, nil
}
