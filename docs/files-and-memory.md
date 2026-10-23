# Files and Memory

To ask about a PDF, stream its bytes to an Upload, register an Asset, then send a payload containing both a text question and the Asset part. The Agent's model must support PDF input, or the Agent must have a suitable media-understanding model configured; attaching an Asset alone does not make its contents readable. Keep the source reader open until upload returns. This function needs `context`, `crypto/rand`, `fmt`, `os`, `a13n` and `generated`:

```go
func askAboutPDF(ctx context.Context, client *a13n.Client, agentID, filename string) (a13n.RunOutcome, error) {
    file, err := os.Open(filename)
    if err != nil { return a13n.RunOutcome{}, err }
    defer file.Close()
    upload, err := client.Upload(ctx, a13n.UploadFile{
        Name: "report.pdf", ContentType: "application/pdf", Reader: file,
    }, rand.Text())
    if err != nil { return a13n.RunOutcome{}, err }

    api, err := client.API()
    if err != nil { return a13n.RunOutcome{}, err }
    response, err := api.CreateAsset(ctx,
        &generated.CreateAssetParams{},
        generated.AssetCreate{Name: "Report", UploadId: upload.Value.UploadId})
    asset, err := a13n.ParseJSON[generated.Asset](client, response, err, 201)
    if err != nil { return a13n.RunOutcome{}, err }

    input := a13n.Input{Content: []a13n.Part{
        a13n.TextPart("Summarize the attached PDF in two sentences."),
        a13n.AssetPart(asset.Value.Id),
    }}
    interaction, err := client.Agent(agentID).StartInput(ctx, input)
    if err != nil { return a13n.RunOutcome{}, err }
    defer interaction.Close()
    result, err := interaction.Result(ctx)
    if err != nil { return a13n.RunOutcome{}, err }
    if result.Status != a13n.RunCompleted {
        return result, fmt.Errorf("run %s ended %s", result.RunID, result.Status)
    }
    return result, nil
}
```

Read direct `Output` or string-only `Text()` from the returned outcome. Asset registration above is an advanced low-level operation; constructing input parts requires only the root SDK. To download an Asset into a caller-owned writer, import `context`, `io` and `a13n`:

```go
func downloadAsset(ctx context.Context, client *a13n.Client, assetID string, destination io.Writer) (int64, error) {
    response, err := client.Asset(assetID).Download(ctx)
    if err != nil { return 0, err }
    defer response.Close()
    return io.Copy(destination, response.Body)
}
```

`client.Asset(assetID).DownloadTo(ctx, destination)` performs the same streaming copy and closes the HTTP body. Neither helper replaces a destination file atomically; the application owns its writer and any partial-write cleanup. The source Upload reader remains caller-owned on both success and failure. `SendInput` uses the same public `Input` to continue an existing Thread.

## Give an Agent a file Memory

Memory is a different resource from a one-message Asset: create a file Memory, add a path to it, and mount it in the next Thread. A `builder` key needs permission to create the Memory. This example imports `context`, `crypto/rand`, `a13n` and `generated`:

```go
func createNotesMemory(ctx context.Context, client *a13n.Client) (string, error) {
    api, err := client.API()
    if err != nil { return "", err }
    fileType := "postgres"
    response, err := api.CreateMemory(ctx,
        &generated.CreateMemoryParams{},
        generated.MemoryCreate{Name: "Project notes", Type: &fileType})
    memory, err := a13n.ParseJSON[generated.Memory](client, response, err, 201)
    if err != nil { return "", err }
    response, err = api.CreateFile(ctx, memory.Value.Id,
        &generated.CreateFileParams{},
        generated.MemoryFileCreate{Path: "projects/计划 #1%.md", Content: "Project goals"})
    _, err = a13n.ParseJSON[generated.MemoryFile](client, response, err, 201)
    if err != nil { return "", err }
    return memory.Value.Id, nil
}
```

Pass the returned ID into a Thread's initial mounts:

```go
mounts := []a13n.MemoryMount{{MemoryId: memoryID, Name: "notes", Access: "read"}}
interaction, err := client.Agent(agentID).Start(ctx, "Summarize the notes.", a13n.StartOptions{
    Memories: &mounts,
})
if err != nil { return err }
defer interaction.Close()
result, err := interaction.Result(ctx)
if err != nil { return err }
fmt.Println(result.Status, result.Output)
```

The Agent sees the mount under `notes`; a read-only mount does not authorize writes. If you need a different access mode later, update the Thread mount with the generated route. A Run's selected mounts remain frozen even if the Thread changes afterwards. Memory providers can also back record memories; the Service and configured provider determine their behavior.

## Read and edit files safely

The generated API treats a path such as `projects/计划 #1%.md` as **one** path parameter and escapes Unicode, slash, hash and percent characters correctly. For edits, read the file's ETag first and supply `IfMatch`; a stale write raises an API error rather than overwriting another writer:

```go
path := "projects/计划 #1%.md"
response, err := api.ReadFile(ctx, memoryID, path,
    &generated.ReadFileParams{})
current, err := a13n.ParseJSON[generated.MemoryFile](client, response, err, 200)
if err != nil { return err }
etag := current.ETag()
response, err = api.ReplaceFile(ctx, memoryID, path,
    &generated.ReplaceFileParams{IfMatch: &etag},
    generated.MemoryFileReplace{Content: "Updated goals"})
updated, err := a13n.ParseJSON[generated.MemoryFile](client, response, err, 200)
if err != nil { return err }
fmt.Println(updated.Value.Path, updated.Value.Content)
```

This last snippet assumes the `api`, `client`, `ctx` and `memoryID` from above; imports also include `fmt`. To inspect changes, use `ListMemoryRevisions` with `a13n.Pages` (see [pagination](generated-api.md#page-through-a-list)). Its revisions carry numeric `Seq`; `RestoreMemoryRevision` takes that `int` and a conditional ETag when replacing an existing file. A restore can yield `MemoryFileState.File` as **null** (for example restoring a creation operation that removes the file). Check the nullable value before accessing its content; see the [installed consumer](../scripts/acceptance/main.go) for an end-to-end Unicode/history/restore example.

## Select shared Memory Spaces for a Session

A Memory Space owns a managed file Memory and an organization policy. Create/read/update/delete it through the generated `/memory-spaces` operations; its `MemoryId` works with the existing files and revisions API. List filters accept repeated labels and an explicit `Enabled: a13n.Ptr(false)`. Updates and deletes use the Space ETag. The `Organization` update supplies a replacement policy; it is not a recursive patch. `OrganizationPolicy.Trigger` is a native union with `FromAnyChangeTrigger` and `FromChangeCountTrigger` helpers. Preserve zero cooldown and explicit false rather than expanding defaults in the client.

Select Spaces with `SessionMemorySettings` on a Session, or use Agent configuration `Memory` for initial defaults. This helper imports `context`, `a13n` and `generated`:

```go
func createSessionWithSpace(ctx context.Context, client *a13n.Client, spaceID string) (
    a13n.Result[generated.SessionView], error,
) {
    api, err := client.API()
    if err != nil { return a13n.Result[generated.SessionView]{}, err }
    memory := generated.SessionMemorySettings{
        Enabled: a13n.Ptr(true),
        Spaces: a13n.Ptr([]generated.SpaceBinding{{Name: "team", SpaceId: spaceID}}),
    }
    response, err := api.CreateSession(ctx,
        &generated.CreateSessionParams{},
        generated.SessionCreate{Memory: a13n.Value(memory)})
    return a13n.ParseJSON[generated.SessionView](client, response, err, 201)
}
```

Omitted settings inherit or preserve their Service-owned value; explicit `spaces:[]` replaces the selection with no Spaces, and `enabled:false` disables participation. Agent defaults are settled on the first accepted business Run; later Agent edits do not reset that Session. Existing direct Memory mounts remain available independently.

`RunView.MemoryMounts` is now a generated union: use `AsMemoryMount()` for a legacy request mount or `AsFrozenMemoryMount()` for host-resolved Space evidence. Frozen mounts can include nullable `SpaceId` and optional non-null `Roots`. Keep the native union when reserializing readback so those fields survive. Roots and Space identity are host evidence, not fields to copy into a request `MemoryMount`.

Organization status and history are separate reads. `OrganizeSpace` takes no body and returns `202` with `SpaceOrganizationStatus`; `Pending`/`ActiveRunId` are asynchronous observations. Service owns scheduling, thresholds, captured authority and background execution. Ordinary Session/Thread lists exclude `memory_organization` maintenance purpose; Space organization history and attached-Session endpoints provide the corresponding explicit views.
