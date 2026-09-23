package agentasm

import (
	"context"
	"fmt"
	"sync"

	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/artifactstore"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/llmprovider"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

// coreAssembly owns the resources shared by every command-layer composition:
// the global session repository, per-session trace, providers, registry, and
// their cleanup order. Callers add mode-specific tools and prompts.
type coreAssembly struct {
	homeDir   string
	session   *session.Session
	service   *reactservice.ReActService
	registry  *tools.DefaultRegistry
	tracer    telemetry.Tracer
	artifacts tools.ArtifactStore
	cleanup   func()
}

func assembleCore(ctx context.Context, workDir, explicitHome, sessionID string,
	consumer func(*reactservice.ReactEvent), withArtifacts bool) (*coreAssembly, error) {
	homeDir, err := resolveHomeDir(explicitHome)
	if err != nil {
		return nil, err
	}
	repo, err := sessionrepo.NewSqliteSessionRepo(layout.SessionDB(homeDir), layout.SessionRoot(homeDir))
	if err != nil {
		return nil, err
	}
	sess := session.NewSession(sessionID, workDir)
	traceHandle, err := newTraceHandle(ctx, layout.TracingLog(homeDir, sess.ID))
	if err != nil {
		_ = repo.Close()
		return nil, fmt.Errorf("init tracing: %w", err)
	}
	registry := tools.NewDefaultRegistry(traceHandle.Tracer)
	var artifacts tools.ArtifactStore
	if withArtifacts {
		artifacts = artifactstore.New(layout.SessionRoot(homeDir))
	}

	c := config.EnvAndFileConf
	service := reactservice.NewReActService(
		sess,
		repo,
		newMainProvider(),
		llmprovider.NewOpenApiProvider(
			c.CompactionOpenaiApiKey, c.CompactionOpenaiBaseUrl, c.CompactionOpenaiModel,
			c.CompactionOpenaiContextWindow, c.CompactionOpenaiMaxOutputTokens),
		registry,
		consumer,
		traceHandle.Tracer,
		artifacts,
	)
	service.SetWorkDir(workDir)

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			_ = registry.Close()
			_ = repo.Close()
			_ = traceHandle.Shutdown(ctx)
		})
	}
	return &coreAssembly{
		homeDir: homeDir, session: sess, service: service, registry: registry,
		tracer: traceHandle.Tracer, artifacts: artifacts, cleanup: cleanup,
	}, nil
}
