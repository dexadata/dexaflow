---
title: Deploy & operate
linkTitle: Deploy & operate
weight: 40
description: Take Dexaflow to production — deploy from CI, run the Helm chart, upgrade, back up, and keep the scheduler resilient.
cascade: { type: docs }
menu:
  main:
    weight: 40
---

Running Dexaflow in production: promotion from Lite to Pro, the deploy pipeline, and
the day-2 operations that keep a control plane healthy.

<div class="lf-cards">
  <a class="lf-card lf-card--hero" href="/operate/first-pro-dag/">
    <span class="lf-card__badge">Start here</span>
    <span class="lf-card__icon"><i class="fa-solid fa-rocket"></i></span>
    <span class="lf-card__title">Deploy your first Pro DAG</span>
    <span class="lf-card__desc">Take a DAG from Lite to a Kubernetes control plane, end to end.</span>
    <span class="lf-card__more">Go to production →</span>
  </a>
  <a class="lf-card" href="/operate/deploy-prerequisites/">
    <span class="lf-card__icon"><i class="fa-solid fa-list-check"></i></span>
    <span class="lf-card__title">Deploy prerequisites &amp; why shortcuts fail</span>
    <span class="lf-card__desc">Every gate a Pro deploy enforces, its exact error, and the fix.</span>
    <span class="lf-card__more">Check the gates →</span>
  </a>
  <a class="lf-card" href="/operate/cicd-deploy/">
    <span class="lf-card__icon"><i class="fa-solid fa-code-branch"></i></span>
    <span class="lf-card__title">CI/CD &amp; deploy</span>
    <span class="lf-card__desc">Build, push, and register DAGs from CI — GitHub Actions, GitLab, Cloud Build.</span>
    <span class="lf-card__more">Wire up CI/CD →</span>
  </a>
  <a class="lf-card" href="/operate/helm-chart/">
    <span class="lf-card__icon"><i class="fa-solid fa-ship"></i></span>
    <span class="lf-card__title">Helm chart</span>
    <span class="lf-card__desc">Install and configure the Pro control plane on Kubernetes.</span>
    <span class="lf-card__more">Install with Helm →</span>
  </a>
  <a class="lf-card" href="/operate/troubleshooting/">
    <span class="lf-card__icon"><i class="fa-solid fa-stethoscope"></i></span>
    <span class="lf-card__title">Troubleshooting &amp; observability</span>
    <span class="lf-card__desc">Diagnose DAG, scheduler, and executor problems — and find where the logs live.</span>
    <span class="lf-card__more">Diagnose issues →</span>
  </a>
  <div class="lf-card">
    <span class="lf-card__icon"><i class="fa-solid fa-arrow-up-right-dots"></i></span>
    <span class="lf-card__title">Upgrades &amp; backup</span>
    <span class="lf-card__desc">Day-2 lifecycle: <a href="/operate/upgrades/">upgrades</a> and <a href="/operate/backup-restore/">backup &amp; restore</a>.</span>
  </div>
  <div class="lf-card">
    <span class="lf-card__icon"><i class="fa-solid fa-heart-pulse"></i></span>
    <span class="lf-card__title">Resilience &amp; latency</span>
    <span class="lf-card__desc"><a href="/operate/scheduler-resilience/">Scheduler resilience</a>, <a href="/operate/control-plane-ha/">control-plane HA &amp; disruption posture</a>, and <a href="/operate/warm-pools/">warm worker pools</a> — availability and latency.</span>
  </div>
  <div class="lf-card">
    <span class="lf-card__icon"><i class="fa-solid fa-shield-halved"></i></span>
    <span class="lf-card__title">Security &amp; data-path internals</span>
    <span class="lf-card__desc"><a href="/operate/agent-credential-transport/">Agent credential transport</a>, <a href="/operate/pro-tls/">Pro TLS</a>, the <a href="/operate/staging-volume/">staging volume</a>, and <a href="/operate/external-secrets/">external secrets</a>.</span>
  </div>
</div>
