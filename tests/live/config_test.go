//go:build livecloud || liveupload

package live

import (
	"os"
	"testing"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/s3client"
)

func required(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("set %s for the chosen fixture", name)
	}
	return value
}

func fixtureClient(t *testing.T) *s3client.Client {
	t.Helper()
	provider := core.Provider(required(t, "S3S_TEST_PROVIDER"))
	if provider != core.AWS && provider != core.R2 {
		t.Fatal("S3S_TEST_PROVIDER must be aws or r2")
	}
	conn := core.Connection{
		ID: "acceptance", Name: "acceptance", Provider: provider,
		Region: os.Getenv("S3S_TEST_REGION"), Endpoint: os.Getenv("S3S_TEST_ENDPOINT"),
	}
	client, err := s3client.New(conn, s3client.Credentials{
		AccessKeyID:     required(t, "S3S_TEST_ACCESS_KEY_ID"),
		SecretAccessKey: required(t, "S3S_TEST_SECRET_ACCESS_KEY"),
		SessionToken:    os.Getenv("S3S_TEST_SESSION_TOKEN"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
