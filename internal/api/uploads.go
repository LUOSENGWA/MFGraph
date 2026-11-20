package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/LUOSENGWA/MFGraph/internal/api/gen"
	"github.com/LUOSENGWA/MFGraph/internal/core"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

func (h *handlers) partURLs(id string, parts []core.UploadURL) []gen.UploadPartURL {
	links := make([]gen.UploadPartURL, 0, len(parts))
	for _, p := range parts {
		u := p.URL
		if u == "" {
			u = h.prefix + "/uploads/" + id + "/parts?part_number=" + strconv.Itoa(p.Number)
		}
		links = append(links, gen.UploadPartURL{PartNumber: p.Number, Method: http.MethodPut, Url: u})
	}
	return links
}

func (h *handlers) InitUpload(ctx context.Context, req gen.InitUploadRequestObject) (gen.InitUploadResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	if b == nil || b.SizeBytes == nil {
		return nil, NewError(CodeInvalidRequest, "size_bytes is required", nil)
	}
	var hint int64
	if b.PartSizeBytes != nil {
		hint = *b.PartSizeBytes
		if hint < 1 {
			return nil, NewError(CodeInvalidRequest, "part_size_bytes must be positive", nil)
		}
	}
	u, err := svc.InitUpload(ctx, p, core.InitUploadRequest{WorkspaceID: b.WorkspaceId, Path: b.Path, Size: *b.SizeBytes, PartSize: hint, Checksum: str(b.ChecksumSha256), ContentType: str(b.ContentType)}, str(req.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	return gen.InitUpload201JSONResponse{UploadId: u.ID, ExpiresAtUs: u.Expires, PartSizeBytes: u.PartSize, TotalParts: u.Total, Parts: h.partURLs(u.ID, u.Parts)}, nil
}
func (h *handlers) GetUpload(ctx context.Context, req gen.GetUploadRequestObject) (gen.GetUploadResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	st, err := svc.UploadStatus(ctx, p, req.UploadId)
	if err != nil {
		return nil, err
	}
	parts := make([]gen.UploadedPart, 0, len(st.Uploaded))
	for _, v := range st.Uploaded {
		parts = append(parts, gen.UploadedPart{PartNumber: v.Number, Etag: v.ETag, SizeBytes: v.Size})
	}
	u := st.Session
	return gen.GetUpload200JSONResponse{UploadId: u.ID, ExpiresAtUs: u.Expires, PartSizeBytes: u.PartSize, TotalParts: u.Total, Status: u.Status, Uploaded: parts, Missing: append([]int{}, st.Missing...)}, nil
}
func (h *handlers) SignUploadParts(ctx context.Context, req gen.SignUploadPartsRequestObject) (gen.SignUploadPartsResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil || len(req.Body.PartNumbers) == 0 {
		return nil, NewError(CodeInvalidRequest, "part_numbers must include at least one part", nil)
	}
	urls, err := svc.UploadURLs(ctx, p, req.UploadId, req.Body.PartNumbers, str(req.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	return gen.SignUploadParts200JSONResponse{Parts: h.partURLs(req.UploadId, urls)}, nil
}
func (h *handlers) PutUploadPart(ctx context.Context, req gen.PutUploadPartRequestObject) (gen.PutUploadPartResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	part, err := svc.PutPart(ctx, p, req.UploadId, req.Params.PartNumber, req.Body, ContentLengthFrom(ctx))
	if err != nil {
		return nil, err
	}
	return gen.PutUploadPart200Response{Headers: gen.PutUploadPart200ResponseHeaders{ETag: strconv.Quote(part.ETag)}}, nil
}
func (h *handlers) CompleteUpload(ctx context.Context, req gen.CompleteUploadRequestObject) (gen.CompleteUploadResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil || req.Body.Parts == nil {
		return nil, NewError(CodeInvalidRequest, "parts must be an array", nil)
	}
	parts := make([]ports.Part, len(req.Body.Parts))
	for i, v := range req.Body.Parts {
		parts[i] = ports.Part{Number: v.PartNumber, ETag: v.Etag}
	}
	r, err := svc.CompleteUpload(ctx, p, req.UploadId, parts, str(req.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	return gen.CompleteUpload200JSONResponse{UploadId: r.ID, Path: r.Path, SizeBytes: r.Size, BlobHash: r.Hash, Staged: true}, nil
}
func (h *handlers) AbortUpload(ctx context.Context, req gen.AbortUploadRequestObject) (gen.AbortUploadResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	if err := svc.AbortUpload(ctx, p, req.UploadId); err != nil {
		return nil, err
	}
	return gen.AbortUpload204Response{}, nil
}
