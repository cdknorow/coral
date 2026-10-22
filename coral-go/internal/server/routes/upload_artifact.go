package routes

// A pasted or attached image is also an artifact (#5284): the dashboard's POST /api/upload keeps saving the file the agent reads by its path
// and now stores a copy in the artifact store, so the chat can show it from GET /api/artifacts/{id}, which Coral Hub serves (the upload routes are
// local only). The artifact step never fails the upload: whatever stops it is said in the reply and the agent still gets its path.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/diskguard"
)

// dashboardUploadBucket is the hourly window these artifacts count in (artifact_store.go admitUpload): the same limits as an agent's, in a bucket of its own so neither can use up the
// other's. It is one fixed key because only the operator's credential reaches POST /api/upload (an agent token is refused by the route table, identity_routes.go); no field the client
// sends names it.
const dashboardUploadBucket = "dashboard"

// dashboardHubBucket is the prefix of the windows of images sent through Coral Hub (#6088): one per hub account ("dashboard-hub:hub-user-<8 hex>"), or "dashboard-hub:" when the hub sent no account,
// each with half the allowance (windowLimits), and none of them this computer's own.
const dashboardHubBucket = "dashboard-hub:"

// uploadVia says an image came through Coral Hub: Hub is true for any request the connector executed, User the hub account (hub-user-<8 hex>) when the hub sent one, "" otherwise.
type uploadVia struct {
	Hub  bool
	User string
}

func (v uploadVia) bucket() string {
	if !v.Hub {
		return dashboardUploadBucket
	}
	return dashboardHubBucket + v.User
}

// uploadSessionLive says whether a session id is a live session of this server; nil (a test) accepts a safe id. A pasted image's record is made only for a session that exists, so a stream of
// uploads cannot create folders for ids that do not.
var uploadSessionLive func(ctx context.Context, sessionID string) bool

// SetUploadSessionCheck gives the upload handler the live-session lookup.
func SetUploadSessionCheck(fn func(ctx context.Context, sessionID string) bool) {
	uploadSessionLive = fn
}

// uploadArtifact is the artifact half of the upload reply.
type uploadArtifact struct {
	ID        string `json:"id"`
	URI       string `json:"uri"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	Name      string `json:"name"`
	Inline    bool   `json:"inline"` // the chat may draw it as a thumbnail (PNG, JPEG, GIF or WebP); the rest are a chip with a download
}

// inlineImageTypes are the artifact types the chat draws as a thumbnail, as GetUpload's thumbnailTypes.
var inlineImageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// uploadImageType is the type of an uploaded image by its bytes, with the extension only for BMP (no magic number the store sniffs); "" when the bytes are not that image. SVG is not an artifact.
func uploadImageType(ext string, data []byte) string {
	if fam := sniffImageFamily(data); fam != "" && fam != "image/heif" {
		return fam
	}
	if ext == ".bmp" && bytes.HasPrefix(data, []byte("BM")) {
		return "image/bmp"
	}
	return ""
}

// strippedUpload is the bytes the agent's file and its artifact are made of: a JPEG, PNG or GIF without its metadata (no re-encoding; the JPEG keeps its Orientation, so a portrait photo is not
// on its side), so the artifact's id is the SHA-256 of the file the agent holds. Anything else (WebP, TIFF, BMP, a file that cannot be read as the image it claims) is the original; the artifact step
// decides what to do about those.
func strippedUpload(content []byte) []byte {
	switch sniffImageFamily(content) {
	case "image/jpeg", "image/png", "image/gif":
		if out, _, err := cleanUploadedImage(content); err == nil {
			return out
		}
	}
	return content
}

// shareUploadedImage stores the image as an artifact on the session's record, as a pasted image (uploaded_by owner, source paste). It returns the artifact, or the reason there is none (a
// sentence for the user). data is what strippedUpload gave.
func shareUploadedImage(ctx context.Context, coralDir, sessionID, name, ext string, data []byte, via uploadVia) (*uploadArtifact, string, time.Time) {
	if coralDir == "" {
		return nil, "the artifact store is not available", time.Time{}
	}
	if ext == ".svg" {
		return nil, "SVG is kept for the agent only; it is not shared", time.Time{}
	}
	mt := uploadImageType(ext, data)
	if mt == "" {
		return nil, "this file is not a picture Coral recognises, so it is kept for the agent only", time.Time{}
	}
	if sessionID == "" {
		return nil, "the agent this image is for is not known, so it is kept for the agent only", time.Time{}
	}
	if uploadSessionLive != nil && !uploadSessionLive(ctx, sessionID) {
		return nil, "the agent this image is for is not running here, so it is kept for the agent only", time.Time{}
	}
	if mt != "image/bmp" {
		cleaned, _, err := cleanUploadedImage(data) // a WebP or TIFF with camera or location data is refused here; a JPEG, PNG or GIF is already clean
		if err != nil {
			var meta errImageMetadata
			if errors.As(err, &meta) {
				return nil, string(meta), time.Time{}
			}
			return nil, err.Error(), time.Time{}
		}
		data = cleaned
	}
	if low, _ := diskguard.Low(); low {
		return nil, "the disk Coral stores its data on is almost full", time.Time{}
	}
	if !board.ValidLabel(name, 160) {
		name = "image" + ext
	}
	st := storeFor(coralDir)
	key := via.bucket()
	if ok, retry := st.admitUpload(key); !ok {
		return nil, fmt.Sprintf("the hourly limit for shared images is used up; try again in %s", retry.Round(time.Minute)), time.Now().Add(retry)
	}
	if !st.chargeUpload(key, int64(len(data))) {
		return nil, "the hourly size limit for shared images is used up; try again later", time.Now().Add(uploadWindow)
	}
	id, meta, err := storeArtifactVia(coralDir, sessionID, name, mt, uploadedByOwner, sourcePaste, via.User, data)
	if err != nil {
		if errors.Is(err, errStoreFull) {
			return nil, "the artifact store is full (its limit is in Settings > Storage)", time.Time{}
		}
		return nil, "the image could not be stored as an artifact", time.Time{}
	}
	// The stored name and type are the first uploader's (the sidecar is written once): the reply says what is stored.
	return &uploadArtifact{ID: id, URI: "coral://artifacts/" + id, MediaType: meta.MediaType, Size: meta.Size, Name: meta.Name, Inline: inlineImageTypes[strings.ToLower(meta.MediaType)]}, "", time.Time{}
}

// uploadedByOwner is the upload record's uploaded_by for an image from the dashboard.
const uploadedByOwner = "owner"
