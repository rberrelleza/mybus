package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// secretKey is the JSON key read when the secret holds key/value pairs, which
// is what the Secrets Manager console creates by default.
const secretKey = "FIVEONEONE_TOKEN"

type secretGetter interface {
	GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// fiveOneOneToken returns the 511.org API key. FIVEONEONE_TOKEN_SECRET (a
// Secrets Manager secret name or ARN) takes precedence over FIVEONEONE_TOKEN.
func fiveOneOneToken(ctx context.Context, getenv func(string) string, newClient func(region string) (secretGetter, error)) (string, error) {
	secretID := getenv("FIVEONEONE_TOKEN_SECRET")
	if secretID == "" {
		if token := getenv("FIVEONEONE_TOKEN"); token != "" {
			return token, nil
		}
		return "", errors.New("set FIVEONEONE_TOKEN_SECRET (Secrets Manager) or FIVEONEONE_TOKEN to a 511.org API key")
	}

	region := firstNonEmpty(arnRegion(secretID), getenv("AWS_REGION"), "us-east-1")
	client, err := newClient(region)
	if err != nil {
		return "", err
	}
	out, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretID)})
	if err != nil {
		return "", fmt.Errorf("reading secret %s: %w", secretID, err)
	}
	return parseSecret(aws.ToString(out.SecretString))
}

// parseSecret accepts either the bare key or a JSON object with a
// FIVEONEONE_TOKEN field.
func parseSecret(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "{") {
		var fields map[string]string
		if err := json.Unmarshal([]byte(value), &fields); err != nil {
			return "", fmt.Errorf("secret is not valid JSON: %w", err)
		}
		value = strings.TrimSpace(fields[secretKey])
		if value == "" {
			return "", fmt.Errorf("secret JSON has no %q field", secretKey)
		}
	}
	if value == "" {
		return "", errors.New("secret is empty")
	}
	return value, nil
}

// arnRegion returns the region of a secret ARN, or "" for a plain name.
func arnRegion(id string) string {
	parts := strings.Split(id, ":")
	if len(parts) > 3 && parts[0] == "arn" {
		return parts[3]
	}
	return ""
}

func newSecretsClient(region string) (secretGetter, error) {
	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	return secretsmanager.NewFromConfig(cfg), nil
}
