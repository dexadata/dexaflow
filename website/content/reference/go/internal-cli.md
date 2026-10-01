---
title: "internal/cli"
linkTitle: "internal/cli"
weight: 10
---

```go
import "github.com/neochaotic/leoflow/internal/cli"
```

Package cli implements the leoflow command\-line interface.

## Index

- [Constants](<#constants>)
- [func Execute\(\) int](<#Execute>)
- [func NewRootCommand\(\) \*cobra.Command](<#NewRootCommand>)
- [type Project](<#Project>)
  - [func DiscoverProjects\(workspace string\) \(\[\]Project, error\)](<#DiscoverProjects>)
- [type WorkspaceSpec](<#WorkspaceSpec>)
  - [func ResolveWorkspace\(dir string\) \(\*WorkspaceSpec, error\)](<#ResolveWorkspace>)
  - [func \(w \*WorkspaceSpec\) WatchedPaths\(\) \[\]string](<#WorkspaceSpec.WatchedPaths>)


## Constants

<a name="MaxWorkspaceDepth"></a>MaxWorkspaceDepth caps the recursion DiscoverProjects performs from the workspace root. \<ws\>/a/b/c/d/dag.py is the deepest allowed \(depth 5\); any project nested deeper is silently skipped. See docs/dag\-authoring.md\#discovery\-rules for the rationale \(workspace navigability \+ protection against pathological layouts\).

```go
const MaxWorkspaceDepth = 5
```

<a name="Execute"></a>
## func [Execute](<https://github.com/dexadata/leoflow/blob/main/internal/cli/root.go#L78>)

```go
func Execute() int
```

Execute runs the root command and returns a process exit code.

<a name="NewRootCommand"></a>
## func [NewRootCommand](<https://github.com/dexadata/leoflow/blob/main/internal/cli/root.go#L15>)

```go
func NewRootCommand() *cobra.Command
```

NewRootCommand builds the root leoflow command with its global flags and subcommands.

<a name="Project"></a>
## type [Project](<https://github.com/dexadata/leoflow/blob/main/internal/cli/discover.go#L43-L60>)

Project is a single DAG project discovered in the workspace by DiscoverProjects. The fields are populated from leoflow.yaml \(when present\) or synthesized from the subdirectory's basename otherwise.

```go
type Project struct {
    // Path is the absolute path to the project directory containing dag.py.
    Path string
    // DagID resolves to the yaml's dag_id when present, else the subdir
    // basename.
    DagID string
    // ConfigPath is the absolute path to leoflow.yaml when one exists, or
    // empty when DiscoverProjects synthesized auto-defaults. The lite watcher
    // logs this on every compile so "which config did it pick up?" is
    // greppable.
    ConfigPath string
    // HasYAML reports whether a leoflow.yaml was present in the project dir.
    HasYAML bool
    // Config is the resolved, default-filled config for the project. Always
    // non-nil; when HasYAML is false the struct holds the schema defaults
    // plus DagID set to the subdir basename.
    Config *domain.LeoflowConfig
}
```

<a name="DiscoverProjects"></a>
### func [DiscoverProjects](<https://github.com/dexadata/leoflow/blob/main/internal/cli/discover.go#L80>)

```go
func DiscoverProjects(workspace string) ([]Project, error)
```

DiscoverProjects walks workspace and returns every directory containing a dag.py file \(the project marker\). Each project's config is the leoflow.yaml in the same dir — loaded via loadProjectConfig so schema defaults apply — or, when no yaml exists, a synthesized LeoflowConfig with DagID set to the subdir basename and every other field at its schema default.

The walk caps at MaxWorkspaceDepth from the workspace root and skips both defaultExcludeDirs and any directory whose name begins with a dot. The workspace root itself counts as depth 0 — a root\-level dag.py \+ leoflow.yaml is still recognized as a project for backward compatibility with the single\-DAG layout \(recommended new layout is one project per subdir; see docs/dag\-authoring.md\).

If two discovered projects resolve to the same dag\_id \(whether from yaml or from the subdir\-basename fallback\), the function returns an error naming the id and every colliding path — per simple\-reliable\-then\-grow the lite loop refuses to compile any of them rather than silently letting the latest tick clobber the previous one.

<a name="WorkspaceSpec"></a>
## type [WorkspaceSpec](<https://github.com/dexadata/leoflow/blob/main/internal/cli/workspace.go#L18-L30>)

WorkspaceSpec is the resolved view of a Lite workspace: the list of DAG projects discovered under it plus a synthesized "root" config that single\- cfg consumers \(venv setup, image\-build defaults\) can still rely on without knowing about multi\-DAG. The Path is the absolute workspace directory.

```go
type WorkspaceSpec struct {
    // Path is the absolute path of the workspace directory.
    Path string
    // Projects is every DAG project discovered under Path. Empty when the
    // workspace is empty (caller is expected to scaffold a starter then).
    Projects []Project
    // RootCfg is a synthesized LeoflowConfig representing the workspace as a
    // whole: union of every project's pip dependencies, the highest declared
    // python_version, and otherwise schema defaults. It is the single cfg the
    // venv setup uses; multi-DAG aware code paths should iterate Projects
    // instead.
    RootCfg *domain.LeoflowConfig
}
```

<a name="ResolveWorkspace"></a>
### func [ResolveWorkspace](<https://github.com/dexadata/leoflow/blob/main/internal/cli/workspace.go#L51>)

```go
func ResolveWorkspace(dir string) (*WorkspaceSpec, error)
```

ResolveWorkspace scans the workspace directory and returns its full WorkspaceSpec. It is the single entry point lite uses to convert a directory into "the set of DAGs I am responsible for"; both the watcher and the compile loop call it on every reload.

Behavior:

- Walks subdirs via DiscoverProjects \(max\-depth 5, skips exclude\_paths defaults \+ hidden dirs, fails loud on duplicate dag\_id\).
- Synthesizes RootCfg by unioning every project's Dependencies \(de\-duped\) and picking the highest PythonVersion \(per\-component numeric compare, so "3.10" \> "3.9"\). Detects per\-package version conflicts in the dependency union: two projects pinning the same package to different versions cannot share a venv, so the workspace fails to resolve \(\[\[simple\-reliable\-then\-grow\]\] — loud reject over silent merge\).
- Returns a non\-nil RootCfg even when Projects is empty, so the caller can scaffold against schema defaults without an extra nil\-check.

Discovery errors \(e.g. duplicate dag\_id\) are propagated verbatim so the caller can surface every colliding path to the user.

<a name="WorkspaceSpec.WatchedPaths"></a>
### func \(\*WorkspaceSpec\) [WatchedPaths](<https://github.com/dexadata/leoflow/blob/main/internal/cli/workspace.go#L79>)

```go
func (w *WorkspaceSpec) WatchedPaths() []string
```

WatchedPaths returns the file paths the mtime\-polling watcher should track: every project's leoflow.yaml \(when present\) and dag.py. A save in any of them must trigger a reload, since lite recompiles\+reregisters every project on each reload.

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
