---
title: Get started
linkTitle: Get started
weight: 10
description: Install Dexaflow and run your first DAG in minutes.
cascade: { type: docs }
menu:
  main:
    weight: 10
---

Zero to a running DAG in minutes. Dexaflow **Lite** runs the whole control plane on
one machine, so you can author, compile, and trigger a real DAG before you ever
touch Kubernetes. Pick your starting point below.

<div class="lf-cards">
  <a class="lf-card lf-card--hero" href="/get-started/quickstart/">
    <span class="lf-card__badge">Start here</span>
    <span class="lf-card__icon"><i class="fa-solid fa-bolt"></i></span>
    <span class="lf-card__title">Quickstart</span>
    <span class="lf-card__desc">Get Dexaflow Lite running locally in two commands, then trigger your first run from the browser.</span>
    <span class="lf-card__more">Run it in 2 commands →</span>
  </a>
  <a class="lf-card" href="/get-started/build-your-first-dag/">
    <span class="lf-card__icon"><i class="fa-solid fa-diagram-project"></i></span>
    <span class="lf-card__title">Build your first DAG</span>
    <span class="lf-card__desc">A guided walk through authoring a <code>dag.py</code>, compiling it to an immutable image, and watching it run.</span>
    <span class="lf-card__more">Follow the walkthrough →</span>
  </a>
  <a class="lf-card" href="/get-started/installation/">
    <span class="lf-card__icon"><i class="fa-solid fa-download"></i></span>
    <span class="lf-card__title">Installation</span>
    <span class="lf-card__desc">Install the <code>leoflow</code> CLI and provision the managed Python runtime — the one-command setup.</span>
    <span class="lf-card__more">Install the CLI →</span>
  </a>
</div>

{{% alert title="Stuck on install or first run?" color="info" %}}
The [Troubleshooting & observability](/operate/troubleshooting/) guide covers the
common failure modes and where the logs live.
{{% /alert %}}
