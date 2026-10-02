package api

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	yaml "go.yaml.in/yaml/v3"
)

//go:embed openapi.yaml
var openAPISpec []byte

// scalarHTML renders the Scalar API reference against the embedded spec,
// themed for Dexaflow (ADR 0013). The /docs route is public.
const scalarHTML = `<!doctype html>
<html>
  <head>
    <title>Dexaflow API Reference</title>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
  </head>
  <body>
    <script id="api-reference" data-url="openapi.json"></script>
    <script>
      document.getElementById('api-reference').dataset.configuration =
        JSON.stringify({ theme: 'purple', darkMode: true, _integration: 'go' });
    </script>
    <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
  </body>
</html>`

func docsHandler(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(scalarHTML))
}

func openAPIYAMLHandler(c *gin.Context) {
	c.Data(http.StatusOK, "application/yaml", openAPISpec)
}

func openAPIJSONHandler(c *gin.Context) {
	var doc any
	if err := yaml.Unmarshal(openAPISpec, &doc); err != nil {
		AbortProblem(c, http.StatusInternalServerError, "spec error", err.Error())
		return
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		AbortProblem(c, http.StatusInternalServerError, "spec error", err.Error())
		return
	}
	c.Data(http.StatusOK, "application/json", encoded)
}

// registerDocs mounts the Scalar reference and raw spec routes.
func registerDocs(r gin.IRouter) {
	r.GET("/docs", docsHandler)
	r.GET("/openapi.yaml", openAPIYAMLHandler)
	r.GET("/openapi.json", openAPIJSONHandler)
}
