package authn_test

import (
	"fmt"

	"github.com/dexadata/dexaflow/pkg/authn"
)

// The server authorizes a request by the user's roles and permissions: the
// admin role grants everything, an admin action every action on its
// resource, and the resource "*" every resource.
func ExampleUser_HasPermission() {
	viewer := &authn.User{Permissions: []authn.Permission{{Action: "read", Resource: "dag"}}}
	operator := &authn.User{Permissions: []authn.Permission{{Action: "admin", Resource: "dag_run"}}}
	admin := &authn.User{Roles: []string{"admin"}}

	fmt.Println(viewer.HasPermission("read", "dag"), viewer.HasPermission("edit", "dag"))
	fmt.Println(operator.HasPermission("delete", "dag_run"), operator.HasPermission("read", "dag"))
	fmt.Println(admin.HasPermission("delete", "connection"))
	// Output:
	// true false
	// true false
	// true
}
