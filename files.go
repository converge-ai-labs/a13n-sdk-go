package a13n

import (
	"context"
	"errors"
	"io"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

// Asset binds a Service Asset ID, never owning its stored bytes.
type Asset struct {
	client *Client
	ID     string
}

func (c *Client) Asset(id string) Asset { return Asset{c, id} }

// Download returns an unbuffered, caller-closed body with status and headers.
func (a Asset) Download(ctx context.Context) (*BinaryResult, error) {
	if err := validateClient(a.client, a.ID); err != nil {
		return nil, err
	}
	response, err := a.client.api.ReadAssetContentApiV1AssetsAssetIdContentGet(ctx, a.ID, &generated.ReadAssetContentApiV1AssetsAssetIdContentGetParams{XWorkspaceID: a.client.semanticWorkspace()})
	return binaryResult(a.client, response, err, 200)
}

// Upload streams file bytes without closing the caller's Reader. The request
// key belongs to the logical upload; the SDK never replays an uncertain write.
func (c *Client) Upload(ctx context.Context, file UploadFile, requestKey string) (Result[generated.Upload], error) {
	if c == nil {
		return Result[generated.Upload]{}, errors.New("client is required")
	}
	if requestKey == "" {
		return Result[generated.Upload]{}, errors.New("Idempotency-Key is required")
	}
	contentType, body, err := multipartBody(file)
	if err != nil {
		return Result[generated.Upload]{}, err
	}
	response, err := c.api.CreateUploadApiV1UploadsPostWithBody(ctx, &generated.CreateUploadApiV1UploadsPostParams{IdempotencyKey: requestKey, XWorkspaceID: c.semanticWorkspace()}, contentType, readerOnly{body})
	return jsonResult[generated.Upload](c, response, err, 200)
}

// DownloadTo copies into an application-owned destination, leaving Close and
// atomic replacement policy to the caller. It avoids buffering asset bytes.
func (a Asset) DownloadTo(ctx context.Context, destination io.Writer) (int64, error) {
	if destination == nil {
		return 0, errors.New("destination is required")
	}
	response, err := a.Download(ctx)
	if err != nil {
		return 0, err
	}
	defer response.Close()
	return io.Copy(destination, response.Body)
}
