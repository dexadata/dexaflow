---
title: Contribute
linkTitle: Contribute
weight: 70
description: Contribute to Dexaflow — the workflow, the local dev loops, and how to build these docs.
cascade: { type: docs }
menu:
  main:
    weight: 70
---

How to work on Dexaflow itself.

<div class="lf-cards">
  <a class="lf-card lf-card--hero" href="/contribute/contributing/">
    <span class="lf-card__badge">Start here</span>
    <span class="lf-card__icon"><i class="fa-solid fa-code-pull-request"></i></span>
    <span class="lf-card__title">Contributing</span>
    <span class="lf-card__desc">The workflow, the code-quality standards, and the TDD gate every change passes.</span>
    <span class="lf-card__more">Read the guide →</span>
  </a>
  <a class="lf-card" href="/contribute/local-dev-loop/">
    <span class="lf-card__icon"><i class="fa-solid fa-arrows-rotate"></i></span>
    <span class="lf-card__title">The local dev loop</span>
    <span class="lf-card__desc">The <code>dexaflow lite</code> hot-reload loop for DAGs, and <code>make lite-redeploy</code> for Go changes.</span>
    <span class="lf-card__more">Set up your loop →</span>
  </a>
  <a class="lf-card" href="/contribute/recipes/">
    <span class="lf-card__icon"><i class="fa-solid fa-list-check"></i></span>
    <span class="lf-card__title">Recipes</span>
    <span class="lf-card__desc">Step by step for the common changes: add a migration, record a change, backport to a release branch.</span>
    <span class="lf-card__more">Follow a recipe →</span>
  </a>
  <a class="lf-card" href="/contribute/build-docs/">
    <span class="lf-card__icon"><i class="fa-solid fa-book"></i></span>
    <span class="lf-card__title">Build the docs</span>
    <span class="lf-card__desc">Build and preview this Hugo + Docsy site locally.</span>
    <span class="lf-card__more">Build the site →</span>
  </a>
  <a class="lf-card" href="/contribute/secret-handling/">
    <span class="lf-card__icon"><i class="fa-solid fa-key"></i></span>
    <span class="lf-card__title">Handling secrets</span>
    <span class="lf-card__desc">The two rules — private locality and masked-on-read — every feature that touches a credential must follow (<a href="/project/adrs/0061-secret-locality/">ADR 0061</a>).</span>
    <span class="lf-card__more">Read the rules →</span>
  </a>
  <a class="lf-card" href="/contribute/image-vulnerability-scanning/">
    <span class="lf-card__icon"><i class="fa-solid fa-shield-halved"></i></span>
    <span class="lf-card__title">Image scanning</span>
    <span class="lf-card__desc">Where container CVE findings appear, what blocks a build and what only opens an issue, and how to triage either one.</span>
    <span class="lf-card__more">Read the policy →</span>
  </a>
</div>
