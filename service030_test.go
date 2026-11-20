package a13n

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
	"github.com/oapi-codegen/nullable"
)

func TestCloudflareDeploymentRetryWire(t *testing.T) {
	var seen string
	client := coverageClient(t, func(req *http.Request) *http.Response {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		seen = req.Method + " " + req.URL.EscapedPath() + " " + req.Header.Get("If-Match") + " " + string(body)
		return coverageResponse(202, `{}`)
	})
	api, err := client.API()
	if err != nil {
		t.Fatal(err)
	}
	match := `"dep:3"`
	_, err = api.RetryCloudflareDeployment(context.Background(), "dep one", &generated.RetryCloudflareDeploymentParams{IfMatch: &match}, generated.DeploymentRetry{ApiToken: nullable.NewNullNullable[string]()})
	if err != nil {
		t.Fatal(err)
	}
	want := `POST /proxy/api/v1/cloudflare-deployments/dep%20one/retry "dep:3" {"api_token":null}`
	if seen != want {
		t.Fatalf("request = %q; want %q", seen, want)
	}
}
