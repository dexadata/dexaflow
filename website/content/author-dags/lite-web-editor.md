---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /lite-web-editor.html
# --- end AUTO redirect aliases ---
title: The Lite web editor
linkTitle: Lite web editor
weight: 90
description: Edit and run DAGs from the browser in Dexaflow Lite.
---

Dexaflow **Lite** ships a small built-in code editor so you can edit a DAG project
straight from the browser — no separate IDE, no extra process. It is a
**Lite-only** convenience for the local, single-machine workflow; **Pro**
teams author DAGs in their own editor and ship them through the GitOps flow.

![The Lite web editor: a file tree on the left and a Python DAG open with syntax highlighting](/assets/screenshots/lite-web-editor.png)

{{% alert title="Why it exists, and what it is not" color="info" %}}
The editor is a thin **[Monaco](https://microsoft.github.io/monaco-editor/)**
surface (the engine behind VS Code) with a file tree — enough to open, edit,
save, create, and delete files in your workspace, with **syntax highlighting
for Python and YAML**. It is intentionally *not* a full IDE: there are no
extensions, no integrated terminal, and no debugger. For those, keep using
your own editor against the same workspace folder. See
[ADR 0025](/project/adrs/0025-lite-embedded-web-editor/) for the rationale.
{{% /alert %}}

## Opening it

When you run `dexaflow lite <project>`, the served UI shows a small **IDE** button
(a `< >` icon) in the bottom-right corner. Click it — the editor opens in a **new
tab** at `/ide`, scoped to your project workspace.

![The Dexaflow home with the LITE badge at top-center and the IDE button at bottom-right](/assets/screenshots/lite-ide-button.png)

<figure markdown>
  ![Close-up of the IDE button](/assets/screenshots/lite-ide-button-zoom.png)
  <figcaption>The <strong>IDE</strong> button (a <code>&lt; &gt;</code> icon), bottom-right of every screen.</figcaption>
</figure>

You can also open it directly at `http://localhost:8088/ide` (or your Lite host
and port).

{{% alert title="Deep-link to a file" color="success" %}}
Append `?open=<path>` to jump straight to a file, e.g.
`/ide?open=dag.py` — handy for sharing a link to a specific DAG.
{{% /alert %}}

The editor follows your OS light/dark preference (and the IDE button matches the
UI's accent in either theme). Force a theme with a query parameter:

```text
/ide?theme=light
/ide?theme=dark
```

![The editor in the dark theme, editing a Python DAG](/assets/screenshots/lite-web-editor-dark.png)

## What it can do

| Action | How |
|---|---|
| Browse the workspace | The left panel lists every file and folder (skipping `.git`, `__pycache__`, `node_modules`, …). Folders show a `▸` / `▾` caret — click it to collapse or expand. Your tree shape persists across reloads. |
| Open & edit a file | Click it in the tree; edits happen in the Monaco editor with Python/YAML highlighting. |
| Save | **⌘S / Ctrl-S**, or the **Save** button. |
| Create a file or folder | **New file** / **New folder** — the blue **"Create target"** chip in the header shows where it will land (a selected folder, or the workspace root). |
| Rename | **Rename** — works on the selected file or folder, including moves into another folder by typing the new full path. |
| Delete | **Delete** — removes the selected file or folder. Folder deletes are recursive; the confirmation says so explicitly so a stray click never quietly wipes a tree. |

### IDE invariants

A few small UX rules the editor enforces so the cursor never lies to you:

- **Selection is sticky.** Click a folder once and it becomes the **create
  target** for subsequent **New file** / **New folder** clicks — even after a
  tree refresh. To reset back to the workspace root, click the empty area
  below the tree.
- **The "Create target" chip is the truth.** Whatever path it shows is where
  the next create lands. Read it before clicking the button.
- **Expanded folders are remembered.** The set of open folders is written to
  `localStorage` (`leoflow.ide.expanded`) so reopening the tab restores your
  tree shape. Deep-linking with `?open=<path>` auto-expands every ancestor of
  that path so you can see where it lives.
- **Delete is loud about recursion.** Folder deletes are recursive on the
  server (`os.RemoveAll`); the confirmation prompt says "Delete folder \"X\"
  and ALL its contents?" so you can never confuse it with a single-file
  delete.

Saving a file is exactly like editing it on disk — the `dexaflow lite` watcher
picks up the change and **hot-reloads** the DAG, same as if you had saved from any
editor. (Remember the [reload gotcha](/contribute/local-dev-loop/#the-edit--reload--see-it-cycle):
the open Airflow tab does not auto-refresh DAG *structure* — reload it.)

## Provisioning the editor assets

To keep the binary light, the Monaco bundle (~13 MB) is **not** baked into the
`dexaflow` binary. It is downloaded **once**, pinned and SHA-256-verified, by:

```bash
dexaflow setup            # end-user install
# or, for the from-source contributor loop:
dexaflow lite provision
```

into `~/.dexaflow/assets/monaco/<version>/`. After that first fetch the editor
works **fully offline**.

If the assets are not present yet (for example, an offline install), the `/ide`
page shows a short hint to run `dexaflow setup` instead of a broken screen. The
rest of Lite — scheduling, runs, the API — is unaffected.

## Security

The editor's file API reads and writes **only inside the configured workspace**:
every path is confined to the workspace root, and absolute paths or `..` traversal
are rejected. Like the rest of Lite, it is meant for a **local / internal-network
or VPN** deployment — see the
[Lite security note](/concepts/editions/). It is gated to the Lite edition and is never
registered in Pro.
