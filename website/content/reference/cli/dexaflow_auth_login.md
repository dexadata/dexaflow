---
aliases:
  - /cli/leoflow_auth_login.html
  - /reference/cli/leoflow_auth_login/
title: "dexaflow auth login"
linkTitle: "auth login"
weight: 16
---

Authenticate to a control plane (Pro) and store the token.

### Synopsis

Authenticate to a control plane and save the token for that server.

When the server offers single sign-on and no username or password is given, login opens the browser on the server's sign-in, where you approve the CLI sign-in, and waits for it on a loopback address (127.0.0.1); --no-browser prints the URL instead. On a machine with no browser (an SSH session, or Linux with no display) a server that serves it gets the device sign-in instead, as with --device: login prints a page and a short code, you open the page in a browser on any device, sign in and confirm the code, and login waits for that approval; --sso keeps the browser sign-in. A username or a password (flags, LEOFLOW_USERNAME/LEOFLOW_PASSWORD or --password-stdin) signs in with a password, as does a server without single sign-on. A password is not sent over plain http to a server that is not on this machine without --allow-insecure-password.

```
dexaflow auth login [flags]
```

### Options

```
      --allow-insecure-password   send a password over plain http to a server that is not on this machine (it can be read on the way)
      --device                    sign in with single sign-on in a browser on another device, by a code (for SSH sessions and containers)
  -h, --help                      help for login
      --no-browser                with single sign-on, print the sign-in URL instead of opening a browser (implies --sso)
      --password string           password
      --password-stdin            read the password from stdin instead of --password (avoids ps/shell-history exposure)
      --server string             control plane base URL, over --target (default: the target in use, then server_url from the config file)
      --sso                       sign in with the server's single sign-on in a browser (the default when the server offers it and no username or password is given)
      --timeout duration          how long to wait for the single sign-on approval (default 5m0s)
      --username string           username (signs in with a password)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow auth](/reference/cli/dexaflow_auth/)	 - Manage authentication tokens.

