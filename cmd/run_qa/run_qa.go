// Package run_qa provides the interactive knowledge-base question-answering mode.
package run_qa

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/infrastructure/cliprinter"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

const (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
)

func checkConfig() error {
	c := config.EnvAndFileConf
	if c.OpenaiApiKey == "" || c.OpenaiBaseUrl == "" || c.OpenaiModel == "" {
		return errors.New("openai_api_key / openai_base_url / openai_model are required")
	}
	if c.EmbedOpenaiApiKey == "" || c.EmbedOpenaiBaseUrl == "" || c.EmbedOpenaiModel == "" {
		return errors.New("EMBBED_OPENAI_API_KEY / EMBBED_OPENAI_BASE_URL / EMBBED_OPENAI_MODEL are required")
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// newEventConsumer intentionally drops all reasoning chunks. QA mode exposes
// only answer text and lifecycle messages to the terminal.
func newEventConsumer(sendIn func(string)) func(*reactservice.ReactEvent) {
	return func(e *reactservice.ReactEvent) {
		switch e.Type {
		case reactservice.ReActEventTypeChunk:
			chunk := e.ChunkEvent
			if chunk == nil {
				return
			}
			switch chunk.Kind {
			case sharedkernel.ChunkTextStart:
				sendIn(colorGreen + "[LaxCode QA] answer: ")
			case sharedkernel.ChunkTextDelta:
				sendIn(colorGreen + chunk.Delta + colorReset)
			case sharedkernel.ChunkTextEnd:
				sendIn(colorReset + "\n")
			}
		case reactservice.ReActEventTypeRecovery:
			sendIn(fmt.Sprintf("%s[LaxCode QA] %s%s\n", colorYellow, e.Content, colorReset))
		}
	}
}

func formatRuntimeError(err error) string {
	message := fmt.Sprintf("%s\n%s[LaxCode QA] error: %v", colorReset, colorRed, err)
	if errors.Is(err, reactservice.ErrPersistRequestContext) {
		message += "\n[LaxCode QA] 会话状态保存未完成；可以继续输入，系统会在下次对话开始前先恢复上一轮。"
	}
	return message + colorReset + "\n"
}

func Run() {
	if err := checkConfig(); err != nil {
		fatal(err)
	}
	workDir := config.CliConf.WorkDir
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outChan := make(chan string)
	inChan := make(chan string)
	sendIn := func(s string) {
		select {
		case inChan <- s:
		case <-ctx.Done():
		}
	}

	assembled, err := agentasm.AssembleQA(ctx, agentasm.QAInput{
		WorkDir:   workDir,
		SessionID: config.CliConf.Session,
		Consumer:  newEventConsumer(sendIn),
	})
	if err != nil {
		fatal(err)
	}
	defer assembled.Cleanup()

	fmt.Printf("session_id: %s\n", assembled.Session.ID)
	fmt.Printf(">>> QA ready, input your question\n")

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case input := <-outChan:
				if _, err := assembled.Service.Answer(ctx, input); err != nil {
					sendIn(formatRuntimeError(err))
				}
				sendIn(cliprinter.StreamEnd)
			}
		}
	}()

	if err := cliprinter.NewTUI(outChan, inChan).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "运行出错:", err)
	}
	cancel()
}
