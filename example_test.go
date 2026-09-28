package a13n_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	a13n "github.com/converge-ai-labs/a13n-sdk-go"
	"github.com/converge-ai-labs/a13n-sdk-go/generated"
	"github.com/oapi-codegen/nullable"
)

// ExampleAgent_Start shows one finite interaction. Its context bounds the
// submission, queue observation, stream and exact Run through the same deadline.
func ExampleAgent_Start() {
	ctx := context.Background()
	client, err := a13n.NewClient(os.Getenv("A13N_SERVICE_URL"), a13n.NewSecret(os.Getenv("A13N_API_TOKEN")), nil)
	if err != nil {
		return
	}
	defer client.Close()
	interaction, err := client.Agent("agt_example").Start(ctx, "Hello", a13n.StartOptions{RequestKey: "hello-001"})
	if err != nil {
		return
	}
	defer interaction.Close()
	for {
		frame, err := interaction.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return
		}
		if delta, ok := frame.(a13n.DeltaFrame); ok {
			fmt.Println(delta.Item)
		}
	}
	outcome, err := interaction.Result(ctx)
	if err != nil {
		return
	}
	if outcome.Status() == generated.RunStatusCompleted {
		fmt.Println(outcome.Output()) // the full structured output, not the last text delta
	}
	// Continue the same Thread using an explicit Agent; it is not owned by an Agent.
	followUp, err := client.Agent("agt_example").Send(ctx, interaction.Thread.ID, "Continue", a13n.SendOptions{RequestKey: "hello-002"})
	if err != nil {
		return
	}
	defer followUp.Close()
	_, _ = followUp.Result(ctx) // no need to drain frames for authoritative readback
}

func ExampleAgent_StartPayload() {
	ctx := context.Background()
	client, err := a13n.NewClient(os.Getenv("A13N_SERVICE_URL"), a13n.NewSecret(os.Getenv("A13N_API_TOKEN")), nil)
	if err != nil {
		return
	}
	defer client.Close()
	payload := a13n.TextPayload("Use the mounted memory to answer")
	mounts := []generated.MemoryMount{}
	interaction, err := client.Agent("agt_example").StartPayload(ctx, payload, a13n.StartOptions{
		RequestKey: "memory-001", AgentRevisionId: nullable.NewNullableWithValue("rev_1"), Memories: &mounts,
		Options: &generated.RunOptionsInput{},
	})
	if err != nil {
		return
	}
	defer interaction.Close()
	_, _ = interaction.Result(ctx)
}

func ExampleClient_API() {
	client, err := a13n.NewClient(os.Getenv("A13N_SERVICE_URL"), a13n.NewSecret(os.Getenv("A13N_API_TOKEN")), nil)
	if err != nil {
		return
	}
	defer client.Close()
	api, err := client.API()
	if err != nil {
		return
	}
	// Admin/organization operations use the same generated API and transport.
	response, err := api.HealthHealthzGet(context.Background())
	if err == nil {
		_ = response.Body.Close()
	}
}

func ExamplePages() {
	ctx := context.Background()
	client, err := a13n.NewClient(os.Getenv("A13N_SERVICE_URL"), a13n.NewSecret(os.Getenv("A13N_API_TOKEN")), nil)
	if err != nil {
		return
	}
	defer client.Close()
	api, _ := client.API()
	pages := a13n.Pages(ctx, "", func(ctx context.Context, cursor *string) (a13n.Result[generated.ThreadPage], error) {
		response, err := api.ListThreadsApiV1ThreadsGet(ctx, &generated.ListThreadsApiV1ThreadsGetParams{Cursor: cursor})
		return a13n.ParseJSON[generated.ThreadPage](client, response, err, 200)
	}, func(page generated.ThreadPage) string { return page.NextCursor.GetOrEmpty() })
	for page, err := range pages {
		if err != nil {
			return
		}
		fmt.Println(page.StatusCode, len(page.Value.Items))
	}
}
