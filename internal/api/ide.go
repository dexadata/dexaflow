package api

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/workspace"
)

// idePageHTML is the self-contained editor page (a file tree + a Monaco editor
// loaded from /ide/vs). It is a few KB; Monaco itself is fetched once by
// `dexaflow setup` and served from disk, so the binary stays light (ADR 0025).
//
//go:embed ide_page.html
var idePageHTML []byte

// WorkspaceFS is the workspace-confined filesystem backing the Lite web editor
// (ADR 0025). Every path is relative to the workspace root and confined to it.
type WorkspaceFS interface {
	Tree() ([]workspace.Entry, error)
	Read(rel string) ([]byte, error)
	Write(rel string, data []byte) error
	Create(rel string, dir bool) error
	Move(from, to string) error
	Delete(rel string) error
}

// ideTreeDTO is the response for GET /api/v2/ide/tree.
type ideTreeDTO struct {
	Entries []workspace.Entry `json:"entries"`
}

// ideFileDTO is the response for reading a file.
type ideFileDTO struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ideWriteBody is the PUT payload to overwrite a file's contents.
type ideWriteBody struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ideCreateBody is the POST payload to create a new file or directory.
type ideCreateBody struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
}

// ideMoveBody is the POST payload for /ide/move, the rename / drag-drop op.
type ideMoveBody struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ideInstallExamplesBody picks the target subdir under the workspace where the
// embedded example DAGs get materialized. Empty means "examples".
type ideInstallExamplesBody struct {
	Dir string `json:"dir"`
}

// ideInstallExamplesResp reports what was written so the IDE can refresh its
// tree and the user can see what they got.
type ideInstallExamplesResp struct {
	Installed []string `json:"installed"`
	Skipped   []string `json:"skipped"`
	// SkippedExamples lists example project names that were not materialized
	// because a workspace root project of the same name already exists.
	// Lite's multi-DAG discovery refuses duplicate dag_ids at boot, so
	// installing the colliding example would silently produce a workspace
	// that the next `dexaflow lite` boot rejects. Reporting this lets the
	// IDE surface a "Skipped: bash_pipeline (already exists)" hint
	// instead of leaving the user to discover the collision at startup.
	// See alpha-prep issue #298 (sub-item: IDE dup-detect).
	SkippedExamples []string `json:"skipped_examples"`
}

// registerIDE mounts the Lite web editor: the workspace filesystem API, the
// editor page, and the Monaco assets. When fs is nil — Production, or Lite
// without a workspace configured — nothing is registered, so the editor is
// unavailable (404). Reads require read:dag and mutations write:dag, since the
// workspace holds DAG source. monacoDir is where `dexaflow setup` placed the
// pinned Monaco bundle; when empty or absent the page shows a setup hint.
// examples, when non-nil, backs the "Download examples" button — typically the
// embedded examples.FS shipped by package leoflow.
func registerIDE(r gin.IRouter, store WorkspaceFS, monacoDir string, examples fs.FS) {
	if store == nil {
		return
	}
	r.GET("/api/v2/ide/tree", RequirePermission("read", "dag"), ideTreeHandler(store))
	r.GET("/api/v2/ide/file", RequirePermission("read", "dag"), ideReadHandler(store))
	r.PUT("/api/v2/ide/file", RequirePermission("write", "dag"), ideWriteHandler(store))
	r.POST("/api/v2/ide/file", RequirePermission("write", "dag"), ideCreateHandler(store))
	r.POST("/api/v2/ide/move", RequirePermission("write", "dag"), ideMoveHandler(store))
	r.DELETE("/api/v2/ide/file", RequirePermission("write", "dag"), ideDeleteHandler(store))
	if examples != nil {
		r.POST("/api/v2/ide/examples/install", RequirePermission("write", "dag"), ideInstallExamplesHandler(store, examples))
	}

	r.GET("/ide", idePageHandler())
	r.HEAD("/ide/vs/*filepath", monacoHandler(monacoDir))
	r.GET("/ide/vs/*filepath", monacoHandler(monacoDir))
}

// idePageHandler serves the embedded editor page.
func idePageHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", idePageHTML)
	}
}

// monacoHandler serves the pinned Monaco bundle from dir. The bundle lives in a
// vs/ subdirectory (dir/vs/loader.js, …), and requests come in under /ide/vs/,
// so stripping /ide/ maps /ide/vs/<f> to dir/vs/<f>. When dir is empty or a file
// is missing it returns 404, which the page reads as "Monaco not provisioned"
// and shows a `dexaflow setup` hint. http.Dir confines reads to dir.
func monacoHandler(dir string) gin.HandlerFunc {
	if dir == "" {
		return func(c *gin.Context) { c.Status(http.StatusNotFound) }
	}
	fileServer := http.StripPrefix("/ide/", http.FileServer(http.Dir(dir)))
	return func(c *gin.Context) {
		// Reject directory listings: only serve concrete files.
		rel := filepath.Clean(c.Param("filepath"))
		if rel == "/" || rel == "." {
			c.Status(http.StatusNotFound)
			return
		}
		fileServer.ServeHTTP(c.Writer, c.Request)
	}
}

func ideTreeHandler(store WorkspaceFS) gin.HandlerFunc {
	return func(c *gin.Context) {
		entries, err := store.Tree()
		if err != nil {
			AbortProblem(c, http.StatusInternalServerError, "ide_error", err.Error())
			return
		}
		c.JSON(http.StatusOK, ideTreeDTO{Entries: entries})
	}
}

func ideReadHandler(store WorkspaceFS) gin.HandlerFunc {
	return func(c *gin.Context) {
		rel := c.Query("path")
		if rel == "" {
			AbortProblem(c, http.StatusBadRequest, "invalid_request", "query parameter 'path' is required")
			return
		}
		data, err := store.Read(rel)
		if err != nil {
			abortIDEError(c, err)
			return
		}
		c.JSON(http.StatusOK, ideFileDTO{Path: rel, Content: string(data)})
	}
}

func ideWriteHandler(store WorkspaceFS) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body ideWriteBody
		if err := c.ShouldBindJSON(&body); err != nil || body.Path == "" {
			AbortProblem(c, http.StatusBadRequest, "invalid_request", "a JSON body with a non-empty 'path' is required")
			return
		}
		if err := store.Write(body.Path, []byte(body.Content)); err != nil {
			abortIDEError(c, err)
			return
		}
		c.JSON(http.StatusOK, ideFileDTO(body))
	}
}

func ideCreateHandler(store WorkspaceFS) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body ideCreateBody
		if err := c.ShouldBindJSON(&body); err != nil || body.Path == "" {
			AbortProblem(c, http.StatusBadRequest, "invalid_request", "a JSON body with a non-empty 'path' is required")
			return
		}
		if err := store.Create(body.Path, body.Dir); err != nil {
			abortIDEError(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"path": body.Path, "dir": body.Dir})
	}
}

func ideMoveHandler(store WorkspaceFS) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body ideMoveBody
		if err := c.ShouldBindJSON(&body); err != nil || body.From == "" || body.To == "" {
			AbortProblem(c, http.StatusBadRequest, "invalid_request", "a JSON body with non-empty 'from' and 'to' is required")
			return
		}
		if err := store.Move(body.From, body.To); err != nil {
			abortIDEError(c, err)
			return
		}
		c.JSON(http.StatusOK, body)
	}
}

// ideInstallExamplesHandler materializes the embedded example DAGs (one
// subdir per DAG) under `<dir>/<dag>/` inside the workspace. Files that
// already exist on disk are skipped so a re-install doesn't clobber edits.
func ideInstallExamplesHandler(store WorkspaceFS, examples fs.FS) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body ideInstallExamplesBody
		// Body is optional; default target is "examples".
		_ = c.ShouldBindJSON(&body) //nolint:errcheck // body is optional
		target := body.Dir
		if target == "" {
			target = "examples"
		}
		resp := ideInstallExamplesResp{
			Installed:       []string{},
			Skipped:         []string{},
			SkippedExamples: []string{},
		}

		// Gather workspace top-level directories so the install can skip any
		// example whose dir-name collides with an existing project — Lite's
		// multi-DAG discovery (internal/cli/discover) treats every top-level
		// directory as a candidate project and refuses duplicate dag_ids on
		// boot, so installing the collision would silently break the next
		// startup (alpha-prep #298, observed during prealpha.28 smoke).
		rootProjects := make(map[string]bool)
		if entries, terr := store.Tree(); terr == nil {
			for _, e := range entries {
				if e.IsDir && !strings.Contains(e.Path, "/") {
					rootProjects[e.Name] = true
				}
			}
		}

		// Walk the embedded examples FS. Embedded root is "examples" (matches
		// the source tree), so we strip that prefix and re-anchor under target.
		walkErr := fs.WalkDir(examples, "examples", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p == "examples" {
				return nil
			}
			// First path segment under "examples/" is the example's project
			// name (e.g. "bash_pipeline"). When the user already has a
			// top-level project with that name, skip the whole subtree.
			relUnder := p[len("examples/"):]
			projectName := relUnder
			if i := strings.Index(relUnder, "/"); i > 0 {
				projectName = relUnder[:i]
			}
			if d.IsDir() {
				if rootProjects[projectName] {
					resp.SkippedExamples = append(resp.SkippedExamples, projectName)
					return fs.SkipDir
				}
				return nil
			}

			rel := path.Join(target, relUnder)
			data, rerr := fs.ReadFile(examples, p)
			if rerr != nil {
				return fmt.Errorf("reading embedded %q: %w", p, rerr)
			}
			// Skip if the file already exists on disk — Read returns nil error
			// when the file is found.
			if _, sterr := store.Read(rel); sterr == nil {
				resp.Skipped = append(resp.Skipped, rel)
				return nil
			}
			if werr := store.Write(rel, data); werr != nil {
				return fmt.Errorf("writing %q: %w", rel, werr)
			}
			resp.Installed = append(resp.Installed, rel)
			return nil
		})
		if walkErr != nil {
			AbortProblem(c, http.StatusInternalServerError, "ide_error", walkErr.Error())
			return
		}
		c.JSON(http.StatusOK, resp)
	}
}

func ideDeleteHandler(store WorkspaceFS) gin.HandlerFunc {
	return func(c *gin.Context) {
		rel := c.Query("path")
		if rel == "" {
			AbortProblem(c, http.StatusBadRequest, "invalid_request", "query parameter 'path' is required")
			return
		}
		if err := store.Delete(rel); err != nil {
			abortIDEError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// abortIDEError maps a workspace error to the right status: an unsafe path is a
// client error (400), a missing file is 404, anything else is 500.
func abortIDEError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, workspace.ErrUnsafePath):
		AbortProblem(c, http.StatusBadRequest, "invalid_path", err.Error())
	case errors.Is(err, fs.ErrNotExist):
		AbortProblem(c, http.StatusNotFound, "not_found", err.Error())
	default:
		AbortProblem(c, http.StatusInternalServerError, "ide_error", err.Error())
	}
}
