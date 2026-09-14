package parser

import (
	"fmt"
	"regexp"
	"strings"
)

// PathParamAliasFor returns the URI-token alias for a canonical
// "package.Kind" RestName by lowercasing the Kind portion. This mirrors the
// rule applied by the datamodel migrator and openapi-generator path-param
// aliasing.
//
//	"orgs.Org"       -> "org"
//	"spaces.Space"   -> "space"
//	"aislice.AISlice"-> "aislice"
func PathParamAliasFor(canonical string) string {
	idx := strings.LastIndex(canonical, ".")
	if idx < 0 {
		return strings.ToLower(canonical)
	}
	return strings.ToLower(canonical[idx+1:])
}

// pathParamAliases is a process-wide registry of alias -> canonical mappings
// declared via RestURIs.PathParams across all RestAPISpec values. It lets
// validators (in this package and in the rest sub-package) resolve
// URI-token aliases that don't follow the lowercase-Kind formula —
// e.g. {datacenter} -> datacenters.DataCenters.
//
// The registry is populated as RestAPISpec values are parsed. ExtensionRestAPI
// URIs have no PathParams map of their own, so they rely on this registry to
// recognize aliases declared by the associated node's RestURIs.
var pathParamAliases = map[string]string{}

// RegisterPathParamAlias records a URI-token alias declared by a RestURIs
// PathParams entry. Idempotent for the same alias->canonical pair; conflicting
// remappings are silently ignored here (cross-URI consistency is enforced by
// rest.ValidateRestApiSpec, which fatals on conflicts before reaching us).
func RegisterPathParamAlias(scope, alias, canonical string) {
	if alias == "" || canonical == "" {
		return
	}
	key := fmt.Sprintf("%s/%s", scope, alias)
	pathParamAliases[key] = canonical
}

// ResolvePathParamAlias returns the canonical "package.Kind" RestName for a
// previously registered alias, or "" if none was registered.
func ResolvePathParamAlias(scope, alias string) string {
	key := fmt.Sprintf("%s/%s", scope, alias)
	return pathParamAliases[key]
}

// ResolvePathParamAlias returns the canonical "package.Kind" RestName for a
// previously registered alias, or "" if none was registered.
func GetPathParamAliases(scope string) string {
	outs := make([]string, 0)
	prefix := fmt.Sprintf("%s/", scope)
	for key, value := range pathParamAliases {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		alias := strings.TrimPrefix(key, prefix)
		outs = append(outs, fmt.Sprintf("%s=%s", alias, value))
	}
	return fmt.Sprintf("scope:%s [%s]", scope, strings.Join(outs, ","))
}


// ValidateRequiredParents checks that all non-singleton, non-ignored parent nodes
// appear as path parameters in the given URI. It returns two lists:
//   - missing: parent RestNames that are required but not found in the URI
//   - ignored: parent RestNames that are missing but configured as ignored
func ValidateRequiredParents(pkgFullName string, uri string, crdName string, parentsMap map[string]NodeHelper, ignoredParams []string) (missing []string, ignored []string) {
	r := regexp.MustCompile(`\{([^{}]+)\}`)
	uriParams := r.FindAllStringSubmatch(uri, -1)

	ignoredSet := make(map[string]struct{})
	for _, val := range ignoredParams {
		ignoredSet[val] = struct{}{}
	}

	crdHelper, ok := parentsMap[crdName]
	if !ok {
		return nil, nil
	}

	for _, parentCrd := range crdHelper.Parents {
		parentHelper, ok := parentsMap[parentCrd]
		if !ok {
			continue
		}

		if parentHelper.IsSingleton {
			continue
		}

		parentName := parentHelper.RestName

		if !pathParamExists(pkgFullName, parentName, uriParams) {
			if _, isIgnored := ignoredSet[parentName]; isIgnored {
				ignored = append(ignored, parentName)
			} else {
				missing = append(missing, parentName)
			}
		}
	}

	return missing, ignored
}

// pathParamExists checks if a name exists as a path parameter in the parsed
// URI params. A canonical "package.Kind" RestName is also considered present
// when the URI uses its lowercase-Kind alias (e.g. "{org}" matches
// RestName "orgs.Org"). This supports path-param aliasing for both
// RestURIs (which carry an explicit PathParams map) and ExtensionRestAPI
// URIs (which do not).
func pathParamExists(pkgFullName string, name string, params [][]string) bool {
	formulaAlias := PathParamAliasFor(name)
	for _, p := range params {
		if len(p) < 2 {
			continue
		}
		token := p[1]
		// Direct canonical match or formula-derived alias.
		if token == name || token == formulaAlias {
			return true
		}
		// User-declared alias registered via RestURIs.PathParams (e.g.
		// {datacenter} -> datacenters.DataCenters).
		if ResolvePathParamAlias(pkgFullName, token) == name {
			return true
		}
	}
	return false
}
