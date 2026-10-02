package api

import (
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// The Airflow 3.2.1 UI sends an unauthenticated user to GET /api/v2/auth/login.
// Upstream this redirects into the simple-auth-manager login SPA, which POSTs
// credentials to /auth/token and stores the returned JWT in the "_token" cookie
// (path /) that the rest of the UI reads. Rather than embed that second SPA,
// Leoflow serves a minimal login page honoring the same contract: it POSTs
// /auth/token and returns to `next`. See docs/ui-compatibility.md and ADR 0018.
//
// The cookie itself is the response's, not the page's: /auth/token sets it
// server-side (setSessionCookie), the same way the SSO callback does. The page
// used to write it with document.cookie, which meant a password login could not
// replace a live SSO session and a JWT-only deployment ran with a session cookie
// any script could read.

// loginPageTemplate is a self-contained login form. Its script posts to
// /auth/token, whose response carries the session cookie, and navigates to next.
var loginPageTemplate = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Leoflow — Sign in</title>
<style>
 body{font-family:system-ui,sans-serif;background:#0f172a;color:#e2e8f0;display:flex;
   min-height:100vh;align-items:center;justify-content:center;margin:0}
 form{background:#1e293b;padding:2rem;border-radius:12px;width:320px;box-shadow:0 10px 30px rgba(0,0,0,.4)}
 h1{font-size:1.25rem;margin:0 0 1rem}
 label{display:block;font-size:.8rem;margin:.75rem 0 .25rem;color:#94a3b8}
 input{width:100%;padding:.6rem;border-radius:6px;border:1px solid #334155;background:#0f172a;color:#e2e8f0;box-sizing:border-box}
 button{margin-top:1.25rem;width:100%;padding:.65rem;border:0;border-radius:6px;background:#6366f1;color:#fff;font-weight:600;cursor:pointer}
 .err{color:#f87171;font-size:.8rem;margin-top:.75rem;min-height:1rem}
 .sso{display:block;margin-top:.5rem;padding:.65rem;border-radius:6px;background:#e2e8f0;color:#0f172a;
   font-weight:600;text-align:center;text-decoration:none}
 .or{display:flex;align-items:center;gap:.6rem;margin:1.1rem 0 .2rem;color:#64748b;font-size:.75rem}
 .or::before,.or::after{content:"";flex:1;height:1px;background:#334155}
 .hint{color:#94a3b8;font-size:.75rem;line-height:1.35;margin:.4rem 0 0}
 .ssoerr{background:#7f1d1d;color:#fecaca;border-radius:6px;padding:.6rem .7rem;font-size:.78rem;line-height:1.35;margin:.9rem 0 0}
 details{margin-top:1.1rem}
 summary{color:#94a3b8;font-size:.8rem;cursor:pointer}
 code{font-size:.72rem;color:#cbd5e1}
</style></head><body>
<form id="f" autocomplete="on">
 <h1>Sign in to Leoflow</h1>
{{ if .SSOFailed }} <p class="ssoerr" role="alert">Leoflow could not complete the sign-in on its side, so you are
 not signed in. Nothing you did is wrong and nothing is misconfigured for you: try again. If it keeps
 happening, tell whoever administers this Leoflow, the error is in the control plane's server log.</p>
{{ end }}{{ if .SSORefused }} <p class="ssoerr" role="alert">Single sign-on did not complete, so you are not signed in.
 Try again. If it keeps failing, ask whoever administers this Leoflow: the reason is recorded in
 the control plane's server log and audit trail, and is deliberately not shown here.</p>
{{ end }}{{ if .SSO }} <a class="sso" href="/api/v2/auth/oidc/login?next={{ .NextQuery }}">Sign in with single sign-on</a>
{{ end }}
{{ if .Collapse }} <details>
 <summary>Break-glass sign-in</summary>
 <p class="hint">No break-glass accounts are configured, so no password is accepted here.
 An operator can allow one by adding its address to <code>auth.oidc.break_glass_emails</code>
 (Helm: <code>auth.oidc.breakGlassEmails</code>) and restarting the control plane.</p>
{{ else }}{{ if .SSO }} <div class="or"><span>or</span></div>
 <p class="hint">The form below is for break-glass accounts. If your organization uses
 single sign-on, use the button above.</p>
{{ end }}{{ end }}
 <label for="u">Username</label><input id="u" name="username" autocomplete="username"{{ if .Focus }} autofocus{{ end }}>
 <label for="p">Password</label><input id="p" name="password" type="password" autocomplete="current-password">
 <button type="submit">Sign in</button>
 <div class="err" id="e"></div>
{{ if .Collapse }} </details>
{{ end }}</form>
<script>
 const next = {{ .Next }};
 document.getElementById('f').addEventListener('submit', async (ev) => {
   ev.preventDefault();
   document.getElementById('e').textContent = '';
   try {
     // same-origin is the fetch default; it is named because the session now
     // depends on it. The response's Set-Cookie is the login.
     const r = await fetch('/auth/token', {
       method: 'POST', credentials: 'same-origin',
       headers: {'Content-Type':'application/json'},
       body: JSON.stringify({username: f.username.value, password: f.password.value})
     });
     if (!r.ok) {
       document.getElementById('e').textContent = r.status === 429
         ? 'Too many attempts. Wait about a minute, then try again.'
         : 'Invalid credentials';
       return;
     }
     // The session cookie arrived with the response. The body still carries the
     // access_token for API clients; this page has no use for it and must not
     // try to set the cookie itself, because a script cannot overwrite the
     // HttpOnly one the server just sent.
     //
     // Ask the browser to remember the credentials. A fetch-based login (no
     // native form navigation) does not trigger the "save password?" prompt on
     // its own; the Credential Management API does. Best-effort, guarded.
     if (window.PasswordCredential) {
       try {
         await navigator.credentials.store(new PasswordCredential({
           id: f.username.value, name: f.username.value, password: f.password.value
         }));
       } catch (_) { /* unsupported or denied: fall through */ }
     }
     window.location.replace(next);
   } catch (_) { document.getElementById('e').textContent = 'Sign-in failed'; }
 });
</script></body></html>`))

// loginPageOpts is what the sign-in page needs to know about the deployment. It
// is a struct rather than a parameter list because the three flags interact:
// autoRedirect is meaningless without sso, and the break-glass posture decides
// how the form is presented under both.
type loginPageOpts struct {
	// sso is whether an OIDC flow was discovered at boot, which is the same
	// condition the router uses to register /api/v2/auth/oidc/login.
	sso bool
	// breakGlass is whether any address may use the password form while SSO is on.
	breakGlass bool
	// autoRedirect starts the flow instead of rendering the page, for deployments
	// where the sign-in page is a screen to acknowledge for nothing.
	autoRedirect bool
	// externalSignIn is the operator's sign-in URL (#1288). When set, it takes
	// the place of this page and of autoRedirect.
	externalSignIn string
}

// loginLocalParam reaches the password form on a deployment that auto-redirects.
// It exists because the account that needs the form is the one used when the IdP
// is the thing that is broken, and requiring a values edit and a rollout to
// reach the escape hatch would defeat the hatch.
const loginLocalParam = "local"

// autoRedirectTarget returns where to send the browser instead of rendering, and
// whether to do so.
//
// The guard is the point of this function, not the redirect. Since #1169 a
// refused sign-on answers 302 to this page carrying sso_error, so redirecting
// unconditionally would bounce every denial straight back to the IdP: an
// infinite loop with no surface left to read the error on. The #1161 review
// predicted this when it rejected auto-redirect outright; the marker is what
// makes the feature safe rather than impossible.
//
// It also requires sso, or a deployment with no flow would send every user to a
// route the router never registered.
//
// An external sign-in (#1288) takes precedence and yields to the same two
// markers, so break-glass and a refused sign-on still reach this page.
func (o loginPageOpts) autoRedirectTarget(c *gin.Context) (string, bool) {
	if c.Query(ssoErrorParam) != "" || c.Query(loginLocalParam) != "" {
		return "", false
	}
	next := sanitizeNext(c.Query("next"))
	if o.externalSignIn != "" {
		return withNext(o.externalSignIn, next), true
	}
	if !o.autoRedirect || !o.sso {
		return "", false
	}
	return "/api/v2/auth/oidc/login?next=" + url.QueryEscape(next), true
}

// withNext appends next to the operator's URL as the `next` query parameter,
// keeping whatever query the operator configured. Config validation has
// already required an absolute http(s) URL, so the parse cannot fail on a
// value that reached here; if it somehow does, the URL is used as is.
func withNext(raw, next string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("next", next)
	u.RawQuery = q.Encode()
	return u.String()
}

// sanitizeNext keeps the post-login redirect on this origin: a single-slash
// absolute path only, defaulting to "/". This blocks open redirects (e.g.
// "//evil.com" or "https://evil.com").
func sanitizeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

// loginPageHandler implements GET /api/v2/auth/login: it serves the login page
// (the Airflow UI redirects here when unauthenticated).
//
// sso says whether an OIDC flow was discovered at boot, which is the same
// condition the router uses to register /api/v2/auth/oidc/login: advertising the
// link without it would 404 the user (#1160).
//
// breakGlass says whether any address is allowed to use the password form while
// SSO is on. Under provider: oidc with an empty allowlist the gate admits NOBODY
// (newBreakGlass), so presenting the form as the primary control, with the focus
// in it, offers a way in that cannot work and answers every attempt with
// "Invalid credentials" - the same answer a wrong password gets. The form stays
// in the page, so adding an account needs no release, but it collapses and says
// which setting turns it on.
func loginPageHandler(o loginPageOpts) gin.HandlerFunc {
	return func(c *gin.Context) {
		if target, ok := o.autoRedirectTarget(c); ok {
			c.Redirect(http.StatusFound, target)
			return
		}
		c.Status(http.StatusOK)
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Header("Cache-Control", "no-cache")
		next := sanitizeNext(c.Query("next"))
		// Next is embedded as a JS string literal. NextQuery goes into an href, and
		// html/template URL-escapes a value in that context ITSELF, so escaping it
		// here too produced next=%252Fdags: the OIDC handler then saw a literal
		// "%2Fdags", sanitizeNext refused it as not absolute, and every SSO login
		// landed on the root instead of the page the user asked for. Pass the
		// sanitized path through raw and let the template do the escaping once.
		// sanitizeNext has already refused anything that is not a single-slash
		// absolute path, so neither field can carry an off-origin redirect.
		if err := loginPageTemplate.Execute(c.Writer, struct {
			Next      template.JS
			NextQuery string
			SSO       bool
			// SSORefused says the user arrived from a single sign-on this
			// deployment refused, and SSOFailed from one that broke on our side.
			// They are separate because they send the user to different places:
			// a refusal needs an administrator to change something, a failure
			// needs a retry. Both are gated on SSO, so nobody can make a
			// deployment without a flow claim a sign-on happened at all.
			SSORefused bool
			SSOFailed  bool
			// Collapse hides a form that cannot succeed; Focus puts the cursor in
			// the form only when it is a way in.
			Collapse bool
			Focus    bool
		}{
			Next:       template.JS("'" + template.JSEscapeString(next) + "'"),
			NextQuery:  next,
			SSO:        o.sso,
			SSORefused: o.sso && c.Query(ssoErrorParam) == ssoErrorRefused,
			SSOFailed:  o.sso && c.Query(ssoErrorParam) == ssoErrorServer,
			Collapse:   o.sso && !o.breakGlass,
			Focus:      !o.sso || o.breakGlass,
		}); err != nil {
			AbortProblem(c, http.StatusInternalServerError, "internal error", "could not render login page")
		}
	}
}

// logoutHandler implements GET /api/v2/auth/logout: it clears the _token cookie
// and returns to the login page, or to the operator's external sign-out when
// one is configured (#1288). It clears through the same helper both login
// paths set through, so the deletion can never disagree with the cookie it is
// deleting.
func logoutHandler(insecureCookies bool, externalSignOut string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Both halves are needed and they are about different things. The helper
		// is how the deletion can never disagree with the cookie it deletes.
		clearSessionCookie(c, insecureCookies)
		if externalSignOut != "" {
			c.Redirect(http.StatusFound, externalSignOut)
			return
		}
		// The PAGE, not the flow. With auto_redirect on, the bare sign-in URL is
		// itself a redirect to the IdP, and our sign-out does not touch the IdP
		// session, so a user who signed out would be signed straight back in and
		// the button would appear to do nothing. The marker that reaches the form
		// is the same one break-glass uses.
		c.Redirect(http.StatusFound, "/api/v2/auth/login?"+loginLocalParam+"=1")
	}
}
