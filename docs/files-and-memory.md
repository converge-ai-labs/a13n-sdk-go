# Files and Memory

To ask about a PDF, stream its bytes to an Upload, register an Asset, then send a payload containing both a text question and the Asset part. The Agent's model must support PDF input, or the Agent must have a suitable media-understanding model configured; attaching an Asset alone does not make its contents readable. Keep the source reader open until upload returns. This function needs `context`, `crypto/rand`, `fmt`, `os`, `a13n` and `generated`:

```go
func askAboutPDF(ctx context.Context, client *a13n.Client, agentID, filename string) (generated.RunItems, error) {
    file, err := os.Open(filename)
    if err != nil { return generated.RunItems{}, err }
    defer file.Close()
    upload, err := client.Upload(ctx, a13n.UploadFile{
        Name: "report.pdf", ContentType: "application/pdf", Reader: file,
    }, rand.Text())
    if err != nil { return generated.RunItems{}, err }

    api, err := client.API()
    if err != nil { return generated.RunItems{}, err }
    response, err := api.CreateAssetApiV1AssetsPost(ctx,
        &generated.CreateAssetApiV1AssetsPostParams{},
        generated.AssetCreate{Name: "Report", UploadId: upload.Value.UploadId})
    asset, err := a13n.ParseJSON[generated.Asset](client, response, err, 201)
    if err != nil { return generated.RunItems{}, err }

    var part generated.Part
    if err := part.FromAssetPart(generated.AssetPart{
        Type: generated.AssetPartTypeAsset, AssetId: asset.Value.Id,
    }); err != nil { return generated.RunItems{}, err }
    var question generated.Part
    if err := question.FromTextPart(generated.TextPart{
        Type: "text", Text: "Summarize the attached PDF in two sentences.",
    }); err != nil { return generated.RunItems{}, err }
    interaction, err := client.Agent(agentID).StartPayload(ctx,
        generated.MessagePayload{Content: []generated.Part{question, part}},
        a13n.StartOptions{RequestKey: rand.Text()})
    if err != nil { return generated.RunItems{}, err }
    defer interaction.Close()
    result, err := interaction.Result(ctx)
    if err != nil { return generated.RunItems{}, err }
    if result.Status() != generated.RunStatusCompleted {
        return generated.RunItems{}, fmt.Errorf("run %s ended %s", result.Run.ID, result.Status())
    }
    committed, err := result.Run.Items(ctx)
    if err != nil { return generated.RunItems{}, err }
    return committed.Value, nil
}
```

Inspect the returned `RunItems.Items` to render the answer. To download an Asset into a caller-owned writer, import `context`, `io` and `a13n`:

```go
func downloadAsset(ctx context.Context, client *a13n.Client, assetID string, destination io.Writer) (int64, error) {
    response, err := client.Asset(assetID).Download(ctx)
    if err != nil { return 0, err }
    defer response.Close()
    return io.Copy(destination, response.Body)
}
```

`client.Asset(assetID).DownloadTo(ctx, destination)` performs the same streaming copy and closes the HTTP body. Neither helper replaces a destination file atomically; the application owns its writer and any partial-write cleanup. The source Upload reader remains caller-owned on both success and failure. `SendPayload` uses the same typed `MessagePayload` to continue an existing Thread.

## Give an Agent a file Memory

Memory is a different resource from a one-message Asset: create a file Memory, add a path to it, and mount it in the next Thread. A `builder` key needs permission to create the Memory. This example imports `context`, `crypto/rand`, `a13n` and `generated`:

```go
func createNotesMemory(ctx context.Context, client *a13n.Client) (string, error) {
    api, err := client.API()
    if err != nil { return "", err }
    fileType := "postgres"
    response, err := api.CreateMemoryApiV1MemoriesPost(ctx,
        &generated.CreateMemoryApiV1MemoriesPostParams{},
        generated.MemoryCreate{Name: "Project notes", Type: &fileType})
    memory, err := a13n.ParseJSON[generated.Memory](client, response, err, 201)
    if err != nil { return "", err }
    response, err = api.CreateFileApiV1MemoriesMemoryIdFilesPost(ctx, memory.Value.Id,
        &generated.CreateFileApiV1MemoriesMemoryIdFilesPostParams{},
        generated.MemoryFileCreate{Path: "projects/计划 #1%.md", Content: "Project goals"})
    _, err = a13n.ParseJSON[generated.MemoryFile](client, response, err, 201)
    if err != nil { return "", err }
    return memory.Value.Id, nil
}
```

Pass the returned ID into a Thread's initial mounts:

```go
mounts := []generated.MemoryMount{{MemoryId: memoryID, Name: "notes", Access: generated.MemoryAccessRead}}
interaction, err := client.Agent(agentID).Start(ctx, "Summarize the notes.", a13n.StartOptions{
    RequestKey: rand.Text(), Memories: &mounts,
})
if err != nil { return err }
defer interaction.Close()
result, err := interaction.Result(ctx)
if err != nil { return err }
items, err := result.Run.Items(ctx)
if err != nil { return err }
fmt.Println(items.Value.Items)
```

The Agent sees the mount under `notes`; a read-only mount does not authorize writes. If you need a different access mode later, update the Thread mount with the generated route. A Run's selected mounts remain frozen even if the Thread changes afterwards. Memory providers can also back record memories; the Service and configured provider determine their behavior.

## Read and edit files safely

The generated API treats a path such as `projects/计划 #1%.md` as **one** path parameter and escapes Unicode, slash, hash and percent characters correctly. For edits, read the file's ETag first and supply `IfMatch`; a stale write raises an API error rather than overwriting another writer:

```go
path := "projects/计划 #1%.md"
response, err := api.ReadFileApiV1MemoriesMemoryIdFilesPathGet(ctx, memoryID, path,
    &generated.ReadFileApiV1MemoriesMemoryIdFilesPathGetParams{})
current, err := a13n.ParseJSON[generated.MemoryFile](client, response, err, 200)
if err != nil { return err }
etag := current.ETag()
response, err = api.ReplaceFileApiV1MemoriesMemoryIdFilesPathPut(ctx, memoryID, path,
    &generated.ReplaceFileApiV1MemoriesMemoryIdFilesPathPutParams{IfMatch: &etag},
    generated.MemoryFileReplace{Content: "Updated goals"})
updated, err := a13n.ParseJSON[generated.MemoryFile](client, response, err, 200)
if err != nil { return err }
fmt.Println(updated.Value.Path, updated.Value.Content)
```

This last snippet assumes the `api`, `client`, `ctx` and `memoryID` from above; imports also include `fmt`. To inspect changes, use `ListRevisionsApiV1MemoriesMemoryIdRevisionsGet` with `a13n.Pages` (see [pagination](generated-api.md#page-through-a-list)). Its revisions carry numeric `Seq`; `RestoreRevisionApiV1MemoriesMemoryIdRevisionsSeqRestorePost` takes that `int` and a conditional ETag when replacing an existing file. A restore can yield `MemoryFileState.File` as **null** (for example restoring a creation operation that removes the file). Check the nullable value before accessing its content; see the [installed consumer](../scripts/acceptance/main.go) for an end-to-end Unicode/history/restore example.
