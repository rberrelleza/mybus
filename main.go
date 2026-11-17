// Command mybus runs the My Bus Alexa skill.
//
// On AWS Lambda (AWS_LAMBDA_RUNTIME_API is set) it serves the Lambda runtime
// API; anywhere else it runs an HTTPS web service endpoint that verifies
// Alexa's request signatures. See README.md for configuration.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/rberrelleza/mybus/internal/alexa"
	"github.com/rberrelleza/mybus/internal/skill"
	"github.com/rberrelleza/mybus/internal/store"
	"github.com/rberrelleza/mybus/internal/transit"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx := context.Background()

	token := os.Getenv("FIVEONEONE_TOKEN")
	if token == "" {
		return errors.New("FIVEONEONE_TOKEN must be set to a 511.org API key")
	}
	skillID := firstNonEmpty(os.Getenv("SKILL_ID"), os.Getenv("APPLICATION_ID"))
	if skillID == "" {
		logger.Warn("SKILL_ID is not set: requests for any skill id will be accepted")
	}

	db, err := dynamoStore(ctx)
	if err != nil {
		return err
	}
	s := &skill.Skill{ID: skillID, Transit: transit.NewClient(token), Store: db, Log: logger}

	if os.Getenv("AWS_LAMBDA_RUNTIME_API") != "" {
		lambda.Start(s.Handle)
		return nil
	}
	return serve(logger, s)
}

func dynamoStore(ctx context.Context) (*store.DynamoDB, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(firstNonEmpty(os.Getenv("DYNAMO_REGION"), os.Getenv("AWS_REGION"), "us-east-1")))
	if err != nil {
		return nil, err
	}
	endpoint := os.Getenv("DYNAMO_ENDPOINT")
	client := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	})
	db := &store.DynamoDB{Client: client, Table: firstNonEmpty(os.Getenv("DYNAMO_TABLE"), "sfbus")}
	if isTrue(os.Getenv("DYNAMO_CREATE_TABLE")) {
		if err := db.CreateTable(ctx); err != nil {
			return nil, err
		}
	}
	return db, nil
}

func serve(logger *slog.Logger, s *skill.Skill) error {
	verify := !isTrue(os.Getenv("SKIP_REQUEST_VERIFICATION"))
	if !verify {
		logger.Warn("request signature verification is DISABLED; never do this in production")
	}
	h := &handler{skill: s, verifier: &alexa.Verifier{}, verify: verify, log: logger}

	mux := http.NewServeMux()
	mux.Handle("POST /", h)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	srv := &http.Server{
		Addr:              ":" + firstNonEmpty(os.Getenv("PORT"), "8080"),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	logger.Info("listening", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type handler struct {
	skill    *skill.Skill
	verifier *alexa.Verifier
	verify   bool
	log      *slog.Logger
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if h.verify {
		if err := h.verifier.Verify(r.Header, body); err != nil {
			h.log.Warn("rejected request", "err", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	}

	var req alexa.RequestEnvelope
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	resp, err := h.skill.Handle(r.Context(), &req)
	if err != nil {
		h.log.Warn("rejected request", "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	_ = json.NewEncoder(w).Encode(resp)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func isTrue(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func logLevel() slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(firstNonEmpty(os.Getenv("LOGLEVEL"), "INFO"))); err != nil {
		return slog.LevelInfo
	}
	return l
}
