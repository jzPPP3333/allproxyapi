package ingress

import (
	"strings"
)

// SanitizeToolParameters deeply cleans and normalizes a JSON Schema for Google Gemini/Antigravity protobuf parser.
// It removes unsupported schema keywords (const, default, title, $schema, additionalProperties, etc.)
// and translates keywords like "const" into Google-compatible "enum".
func SanitizeToolParameters(raw map[string]interface{}) map[string]interface{} {
	if raw == nil {
		return map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}
	}

	sanitized, ok := sanitizeNode(raw).(map[string]interface{})
	if !ok {
		return map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}
	}

	// Root of parameters must be type: "object"
	if t, ok := sanitized["type"].(string); !ok || strings.ToLower(t) != "object" {
		sanitized["type"] = "object"
	}
	if _, ok := sanitized["properties"]; !ok {
		sanitized["properties"] = map[string]interface{}{}
	}

	return sanitized
}

func sanitizeNode(val interface{}) interface{} {
	switch v := val.(type) {
	case map[string]interface{}:
		node := make(map[string]interface{}, len(v))
		for k, val := range v {
			node[k] = val
		}

		// 1. Convert "const": val -> "enum": [val]
		if constVal, hasConst := node["const"]; hasConst {
			delete(node, "const")
			if _, hasEnum := node["enum"]; !hasEnum {
				node["enum"] = []interface{}{constVal}
			}
			if _, hasType := node["type"]; !hasType {
				switch constVal.(type) {
				case string:
					node["type"] = "string"
				case float64, int, int64:
					node["type"] = "number"
				case bool:
					node["type"] = "boolean"
				}
			}
		}

		// 2. Normalize "type" array: e.g. ["string", "null"] -> "string" + nullable: true
		if typeArr, isArr := node["type"].([]interface{}); isArr {
			var nonNullType string
			isNullable := false
			for _, t := range typeArr {
				if str, ok := t.(string); ok {
					if str == "null" {
						isNullable = true
					} else if nonNullType == "" {
						nonNullType = str
					}
				}
			}
			if isNullable {
				node["nullable"] = true
			}
			if nonNullType != "" {
				node["type"] = nonNullType
			} else {
				node["type"] = "string"
			}
		}

		// 3. Remove unsupported Google protobuf fields
		disallowedKeys := []string{
			"title", "default", "$schema", "$id", "$comment", "$defs", "definitions",
			"additionalProperties", "patternProperties", "examples", "readOnly", "writeOnly",
			"contentMediaType", "contentEncoding", "dependencies", "dependentRequired",
			"dependentSchemas", "if", "then", "else",
		}
		for _, key := range disallowedKeys {
			delete(node, key)
		}

		// 4. Clean "format" - Google Schema protobuf only allows a limited set of formats
		if fmtVal, hasFmt := node["format"].(string); hasFmt {
			allowedFormats := map[string]bool{
				"int32": true, "int64": true, "float": true, "double": true,
				"byte": true, "date-time": true, "enum": true,
			}
			if !allowedFormats[strings.ToLower(fmtVal)] {
				delete(node, "format")
			}
		}

		// 5. If type is missing and enum is present, infer type from first enum value
		if _, hasType := node["type"]; !hasType {
			if enumArr, hasEnum := node["enum"].([]interface{}); hasEnum && len(enumArr) > 0 {
				switch enumArr[0].(type) {
				case string:
					node["type"] = "string"
				case float64, int, int64:
					node["type"] = "number"
				case bool:
					node["type"] = "boolean"
				}
			}
		}

		// 6. Recursively clean properties
		if props, hasProps := node["properties"].(map[string]interface{}); hasProps {
			cleanProps := make(map[string]interface{}, len(props))
			for propName, propSchema := range props {
				cleanProps[propName] = sanitizeNode(propSchema)
			}
			node["properties"] = cleanProps
		}

		// 7. Recursively clean items
		if items, hasItems := node["items"]; hasItems {
			node["items"] = sanitizeNode(items)
		}

		// 8. Recursively clean oneOf / anyOf / allOf
		for _, listKey := range []string{"oneOf", "anyOf", "allOf"} {
			if listVal, hasList := node[listKey].([]interface{}); hasList {
				cleanList := make([]interface{}, 0, len(listVal))
				for _, elem := range listVal {
					cleanList = append(cleanList, sanitizeNode(elem))
				}
				node[listKey] = cleanList
			}
		}

		return node

	case []interface{}:
		cleanSlice := make([]interface{}, 0, len(v))
		for _, elem := range v {
			cleanSlice = append(cleanSlice, sanitizeNode(elem))
		}
		return cleanSlice

	default:
		return val
	}
}
