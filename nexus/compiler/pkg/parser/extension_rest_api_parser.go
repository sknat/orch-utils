package parser

import (
	"fmt"
	"go/ast"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/vmware-tanzu/graph-framework-for-microservices/compiler/pkg/config"
	"github.com/vmware-tanzu/graph-framework-for-microservices/nexus/nexus"
	"gopkg.in/yaml.v2"
)

// ExtensionRestAPISpec holds a parsed ExtensionRestAPI variable with metadata.
type ExtensionRestAPISpec struct {
	Name            string   // Variable name
	PkgName         string   // Package name
	PkgFullName         string   // Package fullname
	Uri             string   // URI path
	Methods         []string // HTTP methods to proxy (e.g., ["GET", "PUT"]). Empty = all methods.
	OpenAPIPathSpec string   // Raw OpenAPI path spec YAML
	AssociatedNode  string   // Associated node in "pkg.Type" format (e.g., "gns.Gns")
	NodeCRDName     string   // Full CRD name (e.g., "gnses.gns.tsm.tanzu.vmware.com")
}

// ParseExtensionRestAPIs parses all ExtensionRestAPI variables from the given packages.
func ParseExtensionRestAPIs(pkgs Packages) []ExtensionRestAPISpec {
	var specs []ExtensionRestAPISpec
	for _, pkg := range pkgs {
		pkgSpecs := GetExtensionRestAPISpecs(pkg)
		specs = append(specs, pkgSpecs...)
	}
	log.Infof("ParseExtensionRestAPIs: found %d ExtensionRestAPI specs", len(specs))
	return specs
}

// GetExtensionRestAPISpecs extracts ExtensionRestAPI variables from a single package.
func GetExtensionRestAPISpecs(p Package) []ExtensionRestAPISpec {
	var specs []ExtensionRestAPISpec
	for _, nexusSpec := range GetNexusSpecs(p, "nexus.ExtensionRestAPI") {
		extSpec := parseExtensionRestAPI(nexusSpec.Value, nexusSpec.Name, p.Name, p.FullName)
		if err := ValidateExtensionRestAPI(extSpec); err != nil {
			log.Fatalf("ExtensionRestAPI '%s' in package '%s': %v", nexusSpec.Name, p.Name, err)
		}
		specs = append(specs, extSpec)
	}
	return specs
}

// parseExtensionRestAPI extracts fields from an ExtensionRestAPI composite literal.
func parseExtensionRestAPI(v *ast.CompositeLit, varName, pkgName, pkgFullName string) ExtensionRestAPISpec {
	extSpec := ExtensionRestAPISpec{
		Name:    varName,
		PkgName: pkgName,
		PkgFullName: pkgFullName,
	}

	for _, elt := range v.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}

		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}

		switch key.Name {
		case "Uri":
			if lit, ok := kv.Value.(*ast.BasicLit); ok {
				val, err := strconv.Unquote(lit.Value)
				if err != nil {
					log.Fatalf("ExtensionRestAPI '%s': failed to parse Uri: %v", varName, err)
				}
				extSpec.Uri = val
			}
		case "Methods":
			extSpec.Methods = parseMethodsField(kv.Value, varName)
		case "OpenAPIPathSpec":
			if lit, ok := kv.Value.(*ast.BasicLit); ok {
				val, err := strconv.Unquote(lit.Value)
				if err != nil {
					log.Fatalf("ExtensionRestAPI '%s': failed to parse OpenAPIPathSpec string: %v", varName, err)
				}
				extSpec.OpenAPIPathSpec = val
			} else {
				log.Fatalf("ExtensionRestAPI '%s': OpenAPIPathSpec must be a single raw string literal (using backticks). String concatenations are not supported by the parser.", varName)
			}
		}
	}

	return extSpec
}

// parseMethodsField parses the Methods field from an AST expression.
// It handles []nexus.HTTPMethod{http.MethodGet, http.MethodPut, ...}
func parseMethodsField(expr ast.Expr, varName string) []string {
	var methods []string

	compLit, ok := expr.(*ast.CompositeLit)
	if !ok {
		log.Warnf("ExtensionRestAPI '%s': Methods field is not a composite literal", varName)
		return methods
	}

	// Map of http.Method* constants to uppercase method strings
	methodMap := map[string]string{
		"MethodGet":    "GET",
		"MethodPut":    "PUT",
		"MethodPatch":  "PATCH",
		"MethodDelete": "DELETE",
	}

	for _, elt := range compLit.Elts {
		switch e := elt.(type) {
		case *ast.SelectorExpr:
			// Handle http.MethodGet style
			if e.Sel != nil {
				if method, ok := methodMap[e.Sel.Name]; ok {
					methods = append(methods, method)
				}
			}
		case *ast.BasicLit:
			// Handle string literals like "GET"
			val, err := strconv.Unquote(e.Value)
			if err == nil {
				methods = append(methods, strings.ToUpper(val))
			}
		}
	}

	return methods
}

// ValidateExtensionRestAPI validates an ExtensionRestAPISpec.
func ValidateExtensionRestAPI(extSpec ExtensionRestAPISpec) error {
	if extSpec.OpenAPIPathSpec != "" {
		if err := ValidateOpenAPIPathSpec(extSpec.OpenAPIPathSpec); err != nil {
			return err
		}
	}

	if extSpec.Uri == "" {
		return fmt.Errorf("uri is required")
	}
	return nil
}

// ValidateOpenAPIPathSpec validates the OpenAPI path spec YAML.
func ValidateOpenAPIPathSpec(openAPIPathSpec string) error {
	// 1. Parse YAML syntax into a map
	var rawMap map[string]interface{}
	if err := yaml.Unmarshal([]byte(openAPIPathSpec), &rawMap); err != nil {
		return fmt.Errorf("invalid YAML syntax: %v", err)
	}

	// 2. Validate that at least one valid HTTP method is defined
	validMethods := map[string]bool{
		"get":    true,
		"put":    true,
		"delete": true,
		"patch":  true,
	}

	hasMethod := false
	for key := range rawMap {
		if validMethods[key] {
			hasMethod = true
			break
		}
	}

	if !hasMethod {
		return fmt.Errorf("OpenAPIPathSpec must define at least one HTTP method (get, put, patch, delete)")
	}

	return nil
}

// ValidateExtensionRestAPIPathParams validates that all path params in the URI
// reference valid nodes in the associated node's hierarchy.
func ValidateExtensionRestAPIPathParams(spec ExtensionRestAPISpec, parentsMap map[string]NodeHelper) error {
	if spec.AssociatedNode == "" {
		return fmt.Errorf("not associated with any node (missing // nexus-extension-rest-api annotation)")
	}

	if spec.NodeCRDName == "" {
		return fmt.Errorf("associated node CRD name not set")
	}

	nodeHelper, ok := parentsMap[spec.NodeCRDName]
	if !ok {
		return fmt.Errorf("associated node '%s' not found in graph", spec.AssociatedNode)
	}

	// Extract path params from URI using regex {package.Type}
	r := regexp.MustCompile(`\{([^{}]+)\}`)
	matches := r.FindAllStringSubmatch(spec.Uri, -1)

	// Build set of valid node names (the node itself + all parents). Each
	// canonical "package.Kind" RestName is registered together with its
	// lowercase-Kind alias so that aliased URI tokens (e.g. "{org}" for
	// "orgs.Org") validate successfully. ExtensionRestAPI URIs do not carry
	// an explicit PathParams map, so we apply the same convention used by
	// the datamodel migrator and the openapi-generator.
	validNodes := make(map[string]bool)
	addValid := func(canonical string) {
		validNodes[canonical] = true
		validNodes[PathParamAliasFor(canonical)] = true
	}
	addValid(spec.AssociatedNode)

	// Add all parent nodes to valid set
	for _, parentCRD := range nodeHelper.Parents {
		if parentHelper, ok := parentsMap[parentCRD]; ok {
			addValid(parentHelper.RestName)
		}
	}

	// Validate each path param (forward check: params in URI are valid hierarchy nodes)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		pathParam := match[1]

		// Direct match — either canonical "package.Kind" or formula-derived
		// alias (already pre-registered into validNodes above).
		if validNodes[pathParam] {
			continue
		}
		// User-declared alias registered via some RestURIs.PathParams (e.g.
		// {datacenter} -> datacenters.DataCenters). ExtensionRestAPI URIs do
		// not carry their own PathParams map, so they rely on aliases
		// declared by the associated node's RestURIs.
		if canonical := ResolvePathParamAlias(spec.PkgFullName, pathParam); canonical != "" && validNodes[canonical] {
			continue
		}
		return fmt.Errorf("path param {%s} not found in hierarchy of node %s. "+
			"scope:%s Valid nodes:%s aliases:%s",
			pathParam,
			spec.AssociatedNode,
			spec.PkgFullName,
			getValidNodesList(validNodes),
			GetPathParamAliases(spec.PkgFullName),
		)
	}

	// Reverse check: all required (non-singleton, non-ignored) parents must be in URI
	var ignoredParams []string
	if config.ConfigInstance != nil {
		ignoredParams = config.ConfigInstance.IgnoredParentPathParams
	}
	missing, ignored := ValidateRequiredParents(spec.PkgFullName, spec.Uri, spec.NodeCRDName, parentsMap, ignoredParams)
	for _, name := range ignored {
		log.Warnf("ExtensionRestAPI '%s': parent %s not in URI %s, ignoring (configured as ignored parent path param)",
			spec.Name, name, spec.Uri)
	}
	if len(missing) > 0 {
		return fmt.Errorf("required parent path params missing from URI: %v. URI: %s", missing, spec.Uri)
	}

	return nil
}

// getValidNodesList returns a sorted list of valid node names for error messages.
func getValidNodesList(validNodes map[string]bool) string {
	var nodes []string
	for node := range validNodes {
		nodes = append(nodes, node)
	}
	return fmt.Sprintf("[%s]", strings.Join(nodes, ","))
}

// ParseOpenAPIPathSpecToRestAPISpec parses OpenAPIPathSpec YAML and converts it
// to a nexus.RestAPISpec structure for the annotation.
func ParseOpenAPIPathSpecToRestAPISpec(uri, openAPIPathSpec string) (nexus.RestAPISpec, error) {
	restAPISpec := nexus.RestAPISpec{
		Uris: []nexus.RestURIs{
			{
				Uri:     uri,
				Methods: make(nexus.HTTPMethodsResponses),
			},
		},
	}

	if openAPIPathSpec == "" {
		return restAPISpec, nil
	}

	// Parse YAML into map
	var rawMap map[string]interface{}
	if err := yaml.Unmarshal([]byte(openAPIPathSpec), &rawMap); err != nil {
		return restAPISpec, fmt.Errorf("invalid YAML: %v", err)
	}

	// Map of lowercase method names to HTTP method constants
	methodMap := map[string]nexus.HTTPMethod{
		"get":    nexus.HTTPMethod(http.MethodGet),
		"put":    nexus.HTTPMethod(http.MethodPut),
		"delete": nexus.HTTPMethod(http.MethodDelete),
		"patch":  nexus.HTTPMethod(http.MethodPatch),
	}

	// Extract methods and responses
	for key, value := range rawMap {
		method, ok := methodMap[strings.ToLower(key)]
		if !ok {
			continue
		}

		methodData, ok := value.(map[interface{}]interface{})
		if !ok {
			// Method exists but no details - use default responses
			restAPISpec.Uris[0].Methods[method] = nexus.DefaultHTTPGETResponses
			continue
		}

		// Extract responses
		responses := make(nexus.HTTPCodesResponse)
		if responsesData, ok := methodData["responses"]; ok {
			if respMap, ok := responsesData.(map[interface{}]interface{}); ok {
				for code, respData := range respMap {
					var codeInt int
					switch c := code.(type) {
					case int:
						codeInt = c
					case string:
						fmt.Sscanf(c, "%d", &codeInt)
					}

					if codeInt == 0 {
						continue
					}

					description := ""
					if respDetail, ok := respData.(map[interface{}]interface{}); ok {
						if desc, ok := respDetail["description"]; ok {
							description = fmt.Sprintf("%v", desc)
						}
					}

					responses[nexus.ResponseCode(codeInt)] = nexus.HTTPResponse{
						Description: description,
					}
				}
			}
		}

		if len(responses) == 0 {
			// Use default responses if none specified
			responses = nexus.DefaultHTTPGETResponses
		}

		restAPISpec.Uris[0].Methods[method] = responses
	}

	return restAPISpec, nil
}
