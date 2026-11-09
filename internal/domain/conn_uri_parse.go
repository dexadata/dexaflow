package domain

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ParseConnectionURI reads an Airflow connection URI, the AIRFLOW_CONN_<ID>
// form a task consumes, back into a Connection.
//
// It is the inverse of the URI the control plane delivers to tasks, and it
// follows Airflow's own from_uri semantics wherever the two could differ,
// because the value being parsed is one the user wrote for Airflow to read:
//
//   - the scheme carries `-` where the conn_type has `_` (google_cloud_platform
//     and friends are not legal URI schemes), so it is reversed here exactly as
//     the emitter applies it;
//   - userinfo is percent-decoded into login and password;
//   - the path becomes the schema with ONE leading slash removed, which is what
//     Airflow does; note this is lossy for an absolute path like sqlite's
//     `sqlite:///tmp/db`, whose schema comes back as `tmp/db`. Matching Airflow
//     is the right loss to take: the task resolves the connection through
//     Airflow, so Airflow's reading is the meaning;
//   - `__extra__` carries the JSON blob, as the emitter writes it.
//
// No error returned here ever contains the URI: the value is a credential, and
// an error is the most likely thing to be logged, wrapped or printed. Errors
// name the connection and the problem.
//
// An empty conn_type is refused. A connection with no type cannot be delivered
// to a task in a form Airflow will accept, so storing one would replace a
// missing connection with a broken one.
func ParseConnectionURI(connID, uri string) (Connection, error) {
	if strings.TrimSpace(uri) == "" {
		return Connection{}, fmt.Errorf("connection %q: empty URI", connID)
	}
	u, err := url.Parse(uri)
	if err != nil {
		// url.Parse echoes the input it rejected, and the input is a credential.
		// Name the connection and the problem; never the value.
		return Connection{}, fmt.Errorf("connection %q: not a valid URI", connID)
	}
	if u.Scheme == "" {
		return Connection{}, fmt.Errorf("connection %q: URI has no scheme (expected e.g. postgres://user:pass@host:5432/db)", connID)
	}
	// `host:5432/db` parses "successfully": url.Parse reads `host` as the scheme
	// and the rest as an OPAQUE body, which would store conn_type="host" and
	// deliver a connection no task can use. An Airflow connection URI is always
	// hierarchical, so an opaque one is a typo, not a connection.
	if u.Opaque != "" {
		return Connection{}, fmt.Errorf("connection %q: URI is missing `//` after the scheme (expected e.g. postgres://user:pass@host:5432/db)", connID)
	}
	// The degenerate form the emitter produces for a connection carrying only a
	// type (and possibly extra) has no `//` and no host or path; anything else
	// without `//` lost its authority somewhere.
	if !strings.Contains(uri, "://") && (u.Host != "" || u.Path != "") {
		return Connection{}, fmt.Errorf("connection %q: URI is missing `//` after the scheme", connID)
	}
	c := Connection{
		ConnID:   connID,
		ConnType: strings.ReplaceAll(u.Scheme, "-", "_"),
		Host:     u.Hostname(),
	}
	if u.User != nil {
		c.Login = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			c.Password = pw
		}
	}
	if p := u.Port(); p != "" {
		n, perr := strconv.Atoi(p)
		if perr != nil {
			return Connection{}, fmt.Errorf("connection %q: port %q is not a number", connID, p)
		}
		c.Port = &n
	}
	c.Schema = strings.TrimPrefix(u.Path, "/")
	if extra := u.Query().Get("__extra__"); extra != "" {
		c.Extra = extra
	}
	return c, nil
}
