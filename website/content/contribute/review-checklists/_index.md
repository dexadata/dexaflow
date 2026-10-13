---
title: Review checklists
linkTitle: Review checklists
weight: 16
description: What to check when you review a change in an area where knowing the domain is not enough, and where the evidence comes from.
---

A review states what it verified and how. These checklists name the sources
that settle each question in this repository, so a review rests on the code
and its tests rather than on what the reviewer remembers about Airflow,
Kubernetes or the docs.

They complement the rules: the root
[`AGENTS.md`](https://github.com/dexadata/dexaflow/blob/main/AGENTS.md) and the
`AGENTS.md` of the directory a change touches say what a change must do, and
the [recipes](/contribute/recipes/) say how to make it. A release candidate
has its own list of reviews in
[AGENTS.md section 1](https://github.com/dexadata/dexaflow/blob/main/AGENTS.md#1-release-candidate-reviews).

- [Airflow compatibility](airflow-compatibility/): a change to authoring, the
  parser, the API or the embedded UI.
- [Kubernetes behavior](kubernetes-behavior/): a change to pods, the chart, or
  anything that depends on what a cluster does.
- [Docs](docs/): a change to a page under `website/content/`.
- [Developer experience](developer-experience/): a new setting, flag, value,
  command or error message.
