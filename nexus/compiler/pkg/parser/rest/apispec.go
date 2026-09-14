// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package rest

import (
	"fmt"
	"go/ast"
	"go/types"
	"regexp"
	"strconv"

	log "github.com/sirupsen/logrus"
	"github.com/vmware-tanzu/graph-framework-for-microservices/compiler/pkg/config"
	"github.com/vmware-tanzu/graph-framework-for-microservices/compiler/pkg/parser"
	"github.com/vmware-tanzu/graph-framework-for-microservices/nexus/nexus"
)

var uris = map[string]string{}

func GetRestApiSpecs(p parser.Package, httpMethods map[string]nexus.HTTPMethodsResponses,
	httpCodes map[string]nexus.HTTPCodesResponse, parentsMap map[string]parser.NodeHelper) map[string]nexus.RestAPISpec {

	apiSpecs := make(map[string]nexus.RestAPISpec)
	for _, spec := range parser.GetNexusSpecs(p, "nexus.RestAPISpec") {
		apiSpec := nexus.RestAPISpec{}
		for _, elt := range spec.Value.Elts {
			uris := elt.(*ast.KeyValueExpr)

			for _, uri := range uris.Value.(*ast.CompositeLit).Elts {
				restUri := extractApiSpecRestURI(p, uri.(*ast.CompositeLit), httpMethods, httpCodes)
				apiSpec.Uris = append(apiSpec.Uris, restUri)
			}
		}

		apiSpecs[spec.Name] = apiSpec
	}

	return apiSpecs
}

func extractApiSpecRestURI(p parser.Package, uri *ast.CompositeLit, httpMethods map[string]nexus.HTTPMethodsResponses, httpCodes map[string]nexus.HTTPCodesResponse) nexus.RestURIs {
	restUri := nexus.RestURIs{}
	for _, elt := range uri.Elts {
		kv := elt.(*ast.KeyValueExpr)

		switch types.ExprString(kv.Key) {
		case "Uri":
			key, err := strconv.Unquote(types.ExprString(kv.Value))
			if err != nil {
				log.Errorf("Error %v", err)
			}
			restUri.Uri = key
		case "PathParams":
			restUri.PathParams = extractApiSpecPathParams(p, kv)
		case "QueryParams":
			restUri.QueryParams = extractApiSpecQueryParams(kv)
		case "Headers":
			restUri.Headers = extractApiSpecHeaders(kv)
		case "Methods":
			restUri.Methods = extractApiSpecMethods(kv, httpMethods, httpCodes)
		}
	}

	return restUri
}

// extractApiSpecPathParams parses a map[string]string literal where the key is
// the URI path alias (e.g., "org") and the value is the canonical groupKind
// (e.g., "orgs.Org").
func extractApiSpecPathParams(p parser.Package, kv *ast.KeyValueExpr) map[string]string {
	params := make(map[string]string)
	val, ok := kv.Value.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	for _, elt := range val.Elts {
		entry, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		alias, err := strconv.Unquote(types.ExprString(entry.Key))
		if err != nil {
			log.Errorf("Error parsing PathParams alias: %v", err)
			continue
		}
		canonical, err := strconv.Unquote(types.ExprString(entry.Value))
		if err != nil {
			log.Errorf("Error parsing PathParams canonical for alias %q: %v", alias, err)
			continue
		}
		params[alias] = canonical
		// Publish to the parser-wide registry at parse time so downstream
		// validators (notably ExtensionRestAPIPathParams, which runs before
		// ValidateRestApiSpec) can resolve aliases that don't follow the
		// lowercase-Kind formula.
		parser.RegisterPathParamAlias(p.FullName, alias, canonical)
	}
	if len(params) == 0 {
		return nil
	}
	return params
}

func extractApiSpecMethods(methods *ast.KeyValueExpr, httpMethods map[string]nexus.HTTPMethodsResponses, httpCodes map[string]nexus.HTTPCodesResponse) nexus.HTTPMethodsResponses {
	switch val := methods.Value.(type) {
	case *ast.Ident:
		return httpMethods[val.Name]
	case *ast.SelectorExpr:
		return httpMethods[val.Sel.String()]
	case *ast.CompositeLit:
		met := make(nexus.HTTPMethodsResponses)
		for _, elt := range val.Elts {
			kv := elt.(*ast.KeyValueExpr)
			httpKey := extractHttpMethodsKey(kv.Key)
			httpValue := extractHttpMethodsValue(kv.Value, httpCodes)
			met[httpKey] = httpValue
		}
		return met
	}
	return nil
}

func extractApiSpecQueryParams(kv *ast.KeyValueExpr) []string {
	var params []string
	switch val := kv.Value.(type) {
	case *ast.CompositeLit:
		for _, v := range val.Elts {
			lit := v.(*ast.BasicLit)

			param, err := strconv.Unquote(lit.Value)
			if err != nil {
				log.Errorf("Error %v", err)
			}
			params = append(params, param)
		}
	}
	return params
}

func extractApiSpecHeaders(kv *ast.KeyValueExpr) []string {
	var params []string
	switch val := kv.Value.(type) {
	case *ast.CompositeLit:
		for _, v := range val.Elts {
			lit := v.(*ast.BasicLit)

			param, err := strconv.Unquote(lit.Value)
			if err != nil {
				log.Errorf("Error %v", err)
			}
			params = append(params, param)
		}
	}
	return params
}

// aliasRegistry tracks the canonical type each alias maps to across all URIs
// (across all RestAPISpec validations), so the same alias cannot mean different
// things in different URIs.
var aliasRegistry = map[string]string{}

func ValidateRestApiSpec(pkg parser.Package, apiSpec nexus.RestAPISpec, parentsMap map[string]parser.NodeHelper, crdName string) {
	r := regexp.MustCompile(`{([^{}]+)}`)
	crdHelper := parentsMap[crdName]

	for _, uri := range apiSpec.Uris {
		uriRegex, _ := regexp.Compile("{.*?}")
		redactedUri := uriRegex.ReplaceAllString(uri.Uri, "{param}")

		if u, ok := uris[redactedUri]; ok {
			log.Fatalf("RestApiSpec: Duplicate found: %s and %s", u, uri.Uri)
		}

		// Cross-URI consistency: every alias must map to the same canonical type wherever it appears.
		for alias, canonical := range uri.PathParams {
			key := fmt.Sprintf("%s/%s", pkg.FullName, alias)
			if existing, ok := aliasRegistry[key]; ok && existing != canonical {
				log.Fatalf("RestApiSpec: PathParams alias %q is inconsistent — maps to %q in URI %s but already mapped to %q in a previous URI", alias, canonical, uri.Uri, existing)
			}
			aliasRegistry[key] = canonical
			// Publish to the parser-wide registry so downstream validators
			// (notably ExtensionRestAPIPathParams) can resolve aliases that
			// don't follow the lowercase-Kind formula.
			parser.RegisterPathParamAlias(pkg.FullName, alias, canonical)
		}

		// Build a [][]string of "resolved" URI params where each entry is the
		// canonical type for that token (PathParams[token] if aliased, else the
		// token itself). All downstream node/parent existence checks operate on
		// resolved names so they work the same whether the URI uses alias or
		// canonical form.
		rawUriParams := r.FindAllStringSubmatch(uri.Uri, -1)
		uriParams := resolveUriParams(rawUriParams, uri.PathParams)

		// Every {token} in the URI must either be in PathParams or match a
		// known canonical type (some other parent in parentsMap). This is a soft
		// check — failures here only matter when the token doesn't correspond
		// to any known node; existing parent-presence checks below catch the
		// resulting missing-parent error.
		validateUriTokensAgainstPathParams(uri.Uri, rawUriParams, uri.PathParams, parentsMap)

		if _, ok := uri.Methods["LIST"]; ok {
			if nodeExist(crdHelper.RestName, uriParams) || headerExist(crdHelper.RestName, uri.Headers) || queryParamExist(crdHelper.RestName, uri.QueryParams) {
				log.Fatalf("RestApiSpec: Provided node name (%s) cannot be applied as a param because endpoint is a list. URI: %s", crdHelper.RestName, uri.Uri)
			}
		}

		// Check if node name is in multiple locations (URI, Header, Query param)
		// Parent info must exist in exactly ONE location
		inURI := nodeExist(crdHelper.RestName, uriParams)
		inHeader := headerExist(crdHelper.RestName, uri.Headers)
		inQuery := queryParamExist(crdHelper.RestName, uri.QueryParams)
		locations := 0
		if inURI {
			locations++
		}
		if inHeader {
			locations++
		}
		if inQuery {
			locations++
		}
		if locations > 1 {
			log.Fatalf("RestApiSpec: Provided node name (%s) cannot be applied to multiple locations (URI/Header/Query). Found in %d locations. URI: %s", crdHelper.RestName, locations, uri.Uri)
		}

		for _, parentCrd := range crdHelper.Parents {
			parentCrdHelper := parentsMap[parentCrd]
			parentName := parentCrdHelper.RestName

			if parentCrdHelper.IsSingleton {
				continue
			}

			// Check if parent is in multiple locations
			parentInURI := nodeExist(parentName, uriParams)
			parentInHeader := headerExist(parentName, uri.Headers)
			parentInQuery := queryParamExist(parentName, uri.QueryParams)
			parentLocations := 0
			if parentInURI {
				parentLocations++
			}
			if parentInHeader {
				parentLocations++
			}
			if parentInQuery {
				parentLocations++
			}

			if parentLocations > 1 {
				log.Fatalf("RestApiSpec: Provided parent name (%s) cannot be applied to multiple locations (URI/Header/Query). Found in %d locations. URI: %s", parentName, parentLocations, uri.Uri)
			}
		}

		// Check that all required parents are present in at least one location (URI, Header, or QueryParam)
		// Resolve aliases in the URI first so ValidateRequiredParents sees canonical names.
		resolvedUri := resolveUriString(uri.Uri, uri.PathParams)
		missing, ignoredParents := parser.ValidateRequiredParents(pkg.FullName, resolvedUri, crdName, parentsMap, config.ConfigInstance.IgnoredParentPathParams)
		for _, parentName := range ignoredParents {
			if !headerExist(parentName, uri.Headers) && !queryParamExist(parentName, uri.QueryParams) {
				log.Warnf("RestApiSpec: Provided parent name (%s) not found for uri: %s. Ignoring and proceeding, as it is configured as ignored parent path param", parentName, uri.Uri)
			}
		}
		for _, parentName := range missing {
			// Parent was not found in URI by the shared helper; check Headers/QueryParams as fallback
			if headerExist(parentName, uri.Headers) || queryParamExist(parentName, uri.QueryParams) {
				continue
			}
			log.Fatalf("RestApiSpec: Provided parent name (%s) not found for uri: %s", parentName, uri.Uri)
		}

		uris[redactedUri] = uri.Uri
	}
}

// resolveUriParams maps each raw URI token to its canonical type via PathParams.
// Input shape matches r.FindAllStringSubmatch output ([][]string where [_][1] is the token name).
// Output preserves the same shape; only [_][1] is rewritten when aliased.
func resolveUriParams(rawParams [][]string, pathParams map[string]string) [][]string {
	if len(pathParams) == 0 {
		return rawParams
	}
	out := make([][]string, len(rawParams))
	for i, p := range rawParams {
		if len(p) < 2 {
			out[i] = p
			continue
		}
		token := p[1]
		if canonical, ok := pathParams[token]; ok {
			resolved := make([]string, len(p))
			copy(resolved, p)
			resolved[1] = canonical
			out[i] = resolved
		} else {
			out[i] = p
		}
	}
	return out
}

// resolveUriString returns the URI with every aliased {token} replaced by its
// canonical {package.Kind} form. Used to feed shared helpers that operate on
// canonical-form URIs.
func resolveUriString(uri string, pathParams map[string]string) string {
	if len(pathParams) == 0 {
		return uri
	}
	r := regexp.MustCompile(`{([^{}]+)}`)
	return r.ReplaceAllStringFunc(uri, func(match string) string {
		token := match[1 : len(match)-1]
		if canonical, ok := pathParams[token]; ok {
			return "{" + canonical + "}"
		}
		return match
	})
}

// validateUriTokensAgainstPathParams ensures every {token} in the URI either
// appears as a key in PathParams or matches a known canonical type in parentsMap.
// This is a soft check that warns about typos / unmapped aliases.
func validateUriTokensAgainstPathParams(uri string, rawParams [][]string, pathParams map[string]string, parentsMap map[string]parser.NodeHelper) {
	knownCanonical := map[string]struct{}{}
	for _, helper := range parentsMap {
		knownCanonical[helper.RestName] = struct{}{}
	}
	for _, p := range rawParams {
		if len(p) < 2 {
			continue
		}
		token := p[1]
		if _, aliased := pathParams[token]; aliased {
			continue
		}
		if _, known := knownCanonical[token]; known {
			continue
		}
		log.Warnf("RestApiSpec: URI token %q in %s is neither a PathParams alias nor a known canonical type — RBAC and routing may not resolve it", token, uri)
	}
}

func nodeExist(name string, params [][]string) bool {
	for _, p := range params {
		if p[1] == name {
			return true
		}
	}

	return false
}

func queryParamExist(name string, params []string) bool {
	for _, p := range params {
		if p == name {
			return true
		}
	}

	return false
}

func headerExist(name string, params []string) bool {
	for _, p := range params {
		if p == name {
			return true
		}
	}

	return false
}
