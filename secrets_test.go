package main

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

type fakeSecrets struct {
	value  string
	err    error
	gotID  string
	region string
}

func (f *fakeSecrets) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	f.gotID = aws.ToString(in.SecretId)
	if f.err != nil {
		return nil, f.err
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: aws.String(f.value)}, nil
}

func env(values map[string]string) func(string) string {
	return func(k string) string { return values[k] }
}

func TestTokenFromEnv(t *testing.T) {
	newClient := func(string) (secretGetter, error) { t.Fatal("secrets manager should not be used"); return nil, nil }
	got, err := fiveOneOneToken(context.Background(), env(map[string]string{"FIVEONEONE_TOKEN": "plain"}), newClient)
	if err != nil || got != "plain" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestTokenMissing(t *testing.T) {
	if _, err := fiveOneOneToken(context.Background(), env(nil), nil); err == nil {
		t.Fatal("expected an error")
	}
}

func TestTokenFromSecret(t *testing.T) {
	const arn = "arn:aws:secretsmanager:us-west-2:123456789012:secret:mybus/511-AbCdEf"
	for name, c := range map[string]struct {
		value, want string
		wantErr     bool
	}{
		"plain text":     {value: " key123\n", want: "key123"},
		"key/value JSON": {value: `{"FIVEONEONE_TOKEN":"key456"}`, want: "key456"},
		"wrong JSON key": {value: `{"token":"x"}`, wantErr: true},
		"empty":          {value: "", wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeSecrets{value: c.value}
			newClient := func(region string) (secretGetter, error) { fake.region = region; return fake, nil }
			got, err := fiveOneOneToken(context.Background(),
				env(map[string]string{"FIVEONEONE_TOKEN_SECRET": arn, "FIVEONEONE_TOKEN": "ignored", "AWS_REGION": "us-east-1"}), newClient)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %q, %v", got, err)
			}
			if fake.gotID != arn || fake.region != "us-west-2" {
				t.Errorf("id=%q region=%q", fake.gotID, fake.region)
			}
		})
	}
}

func TestTokenSecretByNameUsesLambdaRegion(t *testing.T) {
	fake := &fakeSecrets{value: "k"}
	var region string
	newClient := func(r string) (secretGetter, error) { region = r; return fake, nil }
	if _, err := fiveOneOneToken(context.Background(), env(map[string]string{"FIVEONEONE_TOKEN_SECRET": "mybus/511", "AWS_REGION": "eu-west-1"}), newClient); err != nil {
		t.Fatal(err)
	}
	if region != "eu-west-1" {
		t.Errorf("region = %q", region)
	}
}

func TestTokenSecretError(t *testing.T) {
	fake := &fakeSecrets{err: errors.New("AccessDeniedException")}
	newClient := func(string) (secretGetter, error) { return fake, nil }
	if _, err := fiveOneOneToken(context.Background(), env(map[string]string{"FIVEONEONE_TOKEN_SECRET": "mybus/511"}), newClient); err == nil {
		t.Fatal("expected error")
	}
}
