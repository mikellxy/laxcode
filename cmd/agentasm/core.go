package agentasm

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/ai_models"
	"github.com/mikellxy/laxcode/internal/infrastructure/artifactstore"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/llmprovider"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing"
)

// coreAssembly owns the resources shared by every command-layer composition:
// the global session repository, owned or injected tracer, providers, registry, and
// their cleanup order. Callers add mode-specific tools and prompts.
type coreAssembly struct {
	homeDir   string
	session   *session.Session
	service   *reactservice.ReActService
	registry  *tools.DefaultRegistry
	artifacts tools.ArtifactStore
	cleanup   func()
}

func assembleCore(ctx context.Context, workDir, explicitHome, sessionID string,
	consumer func(*reactservice.ReactEvent), withArtifacts bool, tracer telemetry.Tracer, models *ai_models.Manager) (*coreAssembly, error) {
	homeDir, err := resolveHomeDir(explicitHome)
	if err != nil {
		return nil, err
	}
	if models == nil {
		models, err = ai_models.New(homeDir, nil, ai_models.DefaultOptions())
		if err != nil {
			return nil, err
		}
	}
	repoStart := time.Now()
	repo, err := sessionrepo.NewSqliteSessionRepo(layout.SessionDB(homeDir), layout.SessionRoot(homeDir))
	telemetry.SpanFromContext(ctx).SetAttributes(telemetry.AttrAssembleRepoInitMs.Float64(float64(time.Since(repoStart)) / float64(time.Millisecond)))
	if err != nil {
		return nil, err
	}
	sess := session.NewSession(sessionID, workDir)
	var traceHandle *tracing.Handle
	if tracer == nil {
		traceHandle, err = newTraceHandle(ctx, layout.TracingLog(homeDir, sess.ID))
		if err != nil {
			_ = repo.Close()
			return nil, fmt.Errorf("init tracing: %w", err)
		}
		tracer = traceHandle.Tracer
	}
	registry := tools.NewDefaultRegistry(tracer)
	var artifacts tools.ArtifactStore
	if withArtifacts {
		artifacts = artifactstore.New(layout.SessionRoot(homeDir))
	}

	c := models.Compaction()
	summaryProvider := llmprovider.NewOpenApiProvider(c.OpenaiApiKey, c.OpenaiBaseUrl, c.UpstreamModel, c.ContextWindow, c.MaxOutputTokens).WithReasoningEffort(c.ReasoningEffort)
	if c.AuthType == "oauth" {
		summaryProvider.WithChatGPT(homeDir, c.CredentialRef)
	}
	service := reactservice.NewReActService(
		sess,
		repo,
		newMainProvider(homeDir, models),
		summaryProvider,
		registry,
		consumer,
		tracer,
		artifacts,
	)
	service.SetWorkDir(workDir)

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			_ = registry.Close()
			_ = repo.Close()
			if traceHandle != nil {
				_ = traceHandle.Shutdown(context.WithoutCancel(ctx))
			}
		})
	}
	return &coreAssembly{
		homeDir: homeDir, session: sess, service: service, registry: registry,
		artifacts: artifacts, cleanup: cleanup,
	}, nil
}
