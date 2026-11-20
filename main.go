// Command mybus runs the My Bus Alexa skill on AWS Lambda (provided.al2023
// runtime). See README.md for configuration and docs/deploy-aws-lambda.md for
// deployment.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/rberrelleza/mybus/internal/skill"
	"github.com/rberrelleza/mybus/internal/store"
	"github.com/rberrelleza/mybus/internal/transit"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()}))
	slog.SetDefault(logger)

	s, err := newSkill(context.Background(), logger)
	if err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
	lambda.Start(s.Handle)
}

func newSkill(ctx context.Context, logger *slog.Logger) (*skill.Skill, error) {
	token, err := fiveOneOneToken(ctx, os.Getenv, newSecretsClient)
	if err != nil {
		return nil, err
	}
	skillID := firstNonEmpty(os.Getenv("SKILL_ID"), os.Getenv("APPLICATION_ID"))
	if skillID == "" {
		logger.Warn("SKILL_ID is not set: requests for any skill id will be accepted")
	}

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(firstNonEmpty(os.Getenv("DYNAMO_REGION"), os.Getenv("AWS_REGION"), "us-east-1")))
	if err != nil {
		return nil, err
	}
	db := &store.DynamoDB{Client: dynamodb.NewFromConfig(cfg), Table: firstNonEmpty(os.Getenv("DYNAMO_TABLE"), "sfbus")}

	return &skill.Skill{ID: skillID, Transit: transit.NewClient(token), Store: db, Log: logger}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func logLevel() slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(firstNonEmpty(os.Getenv("LOGLEVEL"), "INFO"))); err != nil {
		return slog.LevelInfo
	}
	return l
}
