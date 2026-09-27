package a13n_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	a13n "github.com/converge-ai-labs/a13n-sdk-go"
	"github.com/converge-ai-labs/a13n-sdk-go/generated"
	"github.com/oapi-codegen/nullable"
)

// These Service examples are compiled as an external consumer. They are not
// executed by go test: provision explicit resources before running them.
func ExampleThreadsResource_Create() {
	client, err := a13n.NewClient("https://service.example", a13n.NewSecret(os.Getenv("A13N_TOKEN")), nil)
	if err != nil {
		panic(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	workspace := client.Resources().Workspaces().Ref("my-workspace")
	submitted, err := workspace.Threads().Create(ctx, generated.NewThread{
		AgentId: "agt_example", Payload: a13n.TextPayload("Explain this project"),
	}, a13n.ThreadsCreateOptions{IdempotencyKey: "unique-key-for-this-submission"})
	if err != nil {
		panic(err)
	}
	if submitted.Run == nil {
		// Acceptance can be queued; observe submitted.Entry instead.
		return
	}
	result, err := submitted.Run.Wait(ctx, 0)
	if err != nil {
		panic(err)
	}
	if result.Value.Status != generated.RunStatusCompleted {
		// Waiting, failed and cancelled need application-specific handling.
		return
	}
	items, err := submitted.Run.Items().Get(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(items.Value.Complete)
}

func ExampleEntryResource_Wait() {
	client, err := a13n.NewClient("https://service.example", a13n.NewSecret(os.Getenv("A13N_TOKEN")), nil)
	if err != nil {
		panic(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	workspace := client.Resources().Workspaces().Ref("my-workspace")
	entry := workspace.Threads().Ref("thd_example").Inbox().Ref("ent_example")
	result, err := entry.Wait(ctx, 0)
	if err != nil {
		panic(err)
	}
	if result.Value.Status != generated.EntryStatusConsumed {
		return
	}
	runID, err := result.Value.AssignedRunId.Get()
	if err != nil {
		panic(err)
	}
	// Consumption is not Run completion; the assigned Run may already exist.
	run, err := workspace.Runs().Ref(runID).Wait(ctx, 0)
	if err != nil {
		panic(err)
	}
	fmt.Println(run.Value.Status)
}

func ExampleRunResource_Resume() {
	client, err := a13n.NewClient("https://service.example", a13n.NewSecret(os.Getenv("A13N_TOKEN")), nil)
	if err != nil {
		panic(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	workspace := client.Resources().Workspaces().Ref("my-workspace")
	original := workspace.Runs().Ref("run_waiting")
	observed, err := original.Wait(ctx, 0)
	if err != nil {
		panic(err)
	}
	if observed.Value.Status != generated.RunStatusWaiting {
		return
	}
	pending, err := observed.Value.Pending.Get()
	if err != nil {
		panic(err)
	}
	if len(pending.Items) != 1 {
		return
	}
	// This example completes one client-owned tool call. Produce its real result
	// in your application before constructing the answer; do not auto-approve it.
	var answer generated.Answer
	err = answer.FromComplete(generated.Complete{
		Action: "complete", ToolCallId: pending.Items[0].ToolCallId,
		Result: map[string]any{"result": "caller-produced tool output"},
	})
	if err != nil {
		panic(err)
	}
	resumed, err := original.Resume(ctx, generated.ResumeRequest{
		Answers: &[]generated.Answer{answer},
	}, a13n.RunResumeOptions{IdempotencyKey: "unique-key-for-this-resume"})
	if err != nil {
		panic(err)
	}
	// Resume creates a successor. original.Wait still observes the old Run.
	successor := workspace.Runs().Ref(resumed.Value.Id)
	finished, err := successor.Wait(ctx, 0)
	if err != nil {
		panic(err)
	}
	fmt.Println(finished.Value.Status)
}

func ExampleAgentResource_Update() {
	client, err := a13n.NewClient("https://service.example", a13n.NewSecret(os.Getenv("A13N_TOKEN")), nil)
	if err != nil {
		panic(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	agent := client.Resources().Workspaces().Ref("my-workspace").Agents().Ref("agt_example")
	current, err := agent.Get(ctx)
	if err != nil {
		panic(err)
	}
	updated, err := agent.Update(ctx, generated.AgentUpdate{
		Name:        nullable.NewNullableWithValue("Support"),
		Description: nullable.NewNullNullable[string](), // JSON null; Service defines its meaning
	}, a13n.AgentUpdateOptions{IfMatch: current.ETag()})
	if err != nil {
		panic(err)
	} // Reconcile stale CAS; do not blindly retry.
	fmt.Println(updated.ETag())
}

func ExampleThreadResource_Events() {
	client, err := a13n.NewClient("https://service.example", a13n.NewSecret(os.Getenv("A13N_TOKEN")), nil)
	if err != nil {
		panic(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	workspace := client.Resources().Workspaces().Ref("my-workspace")
	thread := workspace.Threads().Ref("thd_example")
	// Events supplies typed frames; Stream().Get is the raw-byte escape hatch.
	stream, err := thread.Events(ctx, a13n.StreamOptions{MaxReconnects: 3})
	if err != nil {
		panic(err)
	}
	defer stream.Close() // Local observation only; never interrupts the Run.
	for {
		frame, err := stream.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			panic(err)
		}
		switch event := frame.(type) {
		case a13n.DeltaFrame:
			fmt.Println(event.Event) // Apply provisional content in your application.
		case a13n.BoundaryFrame:
			fmt.Println(event.RunID, event.Sequence)
		case a13n.ChangedFrame:
			snapshot, err := thread.Get(ctx)
			if err != nil {
				panic(err)
			}
			fmt.Println(snapshot.Value.Id) // Apply the Thread snapshot.
		case a13n.GapFrame:
			snapshot, err := workspace.Runs().Ref(event.RunID).Items().Get(ctx)
			if err != nil {
				panic(err)
			}
			fmt.Println(snapshot.Value.Complete) // Apply authoritative Items.
		case a13n.ResetFrame:
			snapshot, err := workspace.Runs().Ref(event.RunID).Items().Get(ctx)
			if err != nil {
				panic(err)
			}
			fmt.Println(snapshot.Value.Complete) // Replace provisional state.
		}
		// Finish applying the frame/readback before Next acknowledges its cursor.
		// Persist your own durable checkpoint separately.
	}
}

func ExampleAssetContentResource_Get() {
	client, err := a13n.NewClient("https://service.example", a13n.NewSecret(os.Getenv("A13N_TOKEN")), nil)
	if err != nil {
		panic(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	content, err := client.Resources().Workspaces().Ref("my-workspace").Assets().Ref("ast_example").Content().Get(ctx)
	if err != nil {
		panic(err)
	}
	defer content.Close()
	// Stream to an application-owned destination without buffering the file.
	if _, err := io.Copy(io.Discard, content.Body); err != nil {
		panic(err)
	}
}

func ExampleProviderKind() {
	// Aliases retain the generated protocol type identity and readable constants.
	var kind a13n.ProviderKind = a13n.ProviderKindMemory
	member := a13n.MemberKindServiceAccount
	source := a13n.SkillSourceGithub
	options := a13n.OrganizationMembersListOptions{Kind: &member}
	skills := a13n.SkillsListOptions{Source: &source}
	fmt.Println(kind, *options.Kind, *skills.Source)
	// Output: memory service_account github
}
