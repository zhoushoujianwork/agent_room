package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"agent-room/internal/api/relay"
	"agent-room/internal/config"
	"agent-room/internal/io/llm"
	"agent-room/internal/io/memory"
	"agent-room/internal/io/sqlite"
	"agent-room/internal/models"
	"agent-room/internal/service/chat"
	"agent-room/internal/service/summary"
)

// summaryBackend is the store capability the summarizer needs: read recent
// messages and read/write the rolling summary. Both SQLite and in-memory
// stores satisfy it.
type summaryBackend interface {
	models.MessageStore
	models.SummaryStore
}

type RelayApp struct {
	cfg    config.Config
	logger *slog.Logger
}

func NewRelayApp(cfg config.Config, logger *slog.Logger) *RelayApp {
	return &RelayApp{cfg: cfg, logger: logger}
}

func (a *RelayApp) Run(ctx context.Context) error {
	var (
		msgStore        models.MessageStore
		roomStore       models.RoomStore
		attachmentStore models.AttachmentStore
		closeFunc       = func() error { return nil }
	)
	if a.cfg.DBPath == "" {
		// One in-memory store backs both interfaces so an anonymous
		// dev run sees a coherent view of messages + rooms.
		mem := memory.NewStore()
		msgStore = mem
		roomStore = mem
		attachmentStore = mem
		a.logger.Info("relay using in-memory store; history is lost on restart")
	} else {
		sqliteStore, err := sqlite.Open(a.cfg.DBPath)
		if err != nil {
			return err
		}
		msgStore = sqliteStore
		roomStore = sqliteStore
		attachmentStore = sqliteStore
		closeFunc = sqliteStore.Close
		a.logger.Info("relay using sqlite store", slog.String("db", a.cfg.DBPath))
	}
	defer func() { _ = closeFunc() }()

	chatService := chat.NewService(msgStore)
	server := relay.NewServer(a.cfg, chatService, roomStore, a.logger).
		WithAttachments(attachmentStore)

	// Optional rolling-summary feature: only when both an LLM is configured
	// and the store can persist summaries. SQLite satisfies SummaryStore; the
	// in-memory store does too, so dev runs get summaries in-process as well.
	if a.cfg.LLM.Enabled() {
		if summaryStore, ok := msgStore.(summaryBackend); ok {
			llmClient := llm.New(a.cfg.LLM.APIKey, a.cfg.LLM.BaseURL, a.cfg.LLM.Model)
			summarizer := summary.New(summaryStore, llmClient, a.cfg.LLM.SummaryEvery, a.cfg.LLM.SummaryInterval, a.logger)
			server.WithSummary(summaryStore, summarizer)
			a.logger.Info("llm room summaries enabled", slog.String("model", a.cfg.LLM.Model))
		} else {
			a.logger.Warn("llm configured but store does not support summaries; disabled")
		}
	} else {
		a.logger.Info("llm room summaries disabled (LLM_API_KEY not set)")
	}

	if a.cfg.GitHub.Enabled() {
		a.logger.Info("github oauth enabled")
	} else {
		a.logger.Info("github oauth disabled (auth env not set); anonymous-only mode")
	}
	httpServer := &http.Server{
		Addr:              a.cfg.Addr,
		Handler:           server.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		a.logger.Info("relay listening", slog.String("addr", a.cfg.Addr))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
