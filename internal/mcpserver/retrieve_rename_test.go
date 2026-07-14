package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestU5b_VocabularyRename_CleanCut is DONE MEANS (f): pins U5b's clean-cut
// API vocabulary rename (specs/U5_api_retrieve.md) both at the tool
// registration level (search_tags exists, search_antigens does not) and at
// the JSON level (retrieve's input schema advertises tags/include_links, not
// antigens/include_siblings; retrieve's actual response uses matched_tags).
// No deprecation alias: the old names must be entirely gone, not merely
// accepted-and-ignored.
func TestU5b_VocabularyRename_CleanCut(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e, DefaultRole)

	client := connectInMemoryT(t, s)
	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	var (
		foundSearchTags     bool
		foundSearchAntigens bool
		retrieveSchemaProps map[string]any
	)
	for _, tool := range tools.Tools {
		switch tool.Name {
		case "search_tags":
			foundSearchTags = true
		case "search_antigens":
			foundSearchAntigens = true
		case "retrieve":
			retrieveSchemaProps = schemaPropertiesT(t, tool.InputSchema)
		}
	}

	if !foundSearchTags {
		t.Errorf("tools/list missing search_tags (want the U5b-renamed tool present): %+v", toolNamesT(tools))
	}
	if foundSearchAntigens {
		t.Errorf("tools/list still has search_antigens; want it gone (clean cut, no alias): %+v", toolNamesT(tools))
	}

	if retrieveSchemaProps == nil {
		t.Fatalf("retrieve tool not found in tools/list: %+v", toolNamesT(tools))
	}
	if _, ok := retrieveSchemaProps["tags"]; !ok {
		t.Errorf("retrieve input schema missing %q property: %+v", "tags", retrieveSchemaProps)
	}
	if _, ok := retrieveSchemaProps["include_links"]; !ok {
		t.Errorf("retrieve input schema missing %q property: %+v", "include_links", retrieveSchemaProps)
	}
	if _, ok := retrieveSchemaProps["link_depth"]; !ok {
		t.Errorf("retrieve input schema missing %q property: %+v", "link_depth", retrieveSchemaProps)
	}
	if _, ok := retrieveSchemaProps["max_linked_beads"]; !ok {
		t.Errorf("retrieve input schema missing %q property: %+v", "max_linked_beads", retrieveSchemaProps)
	}
	if _, ok := retrieveSchemaProps["include_unattested"]; !ok {
		t.Errorf("retrieve input schema missing %q property: %+v", "include_unattested", retrieveSchemaProps)
	}
	if _, ok := retrieveSchemaProps["antigens"]; ok {
		t.Errorf("retrieve input schema still has legacy %q property; want it gone (clean cut): %+v", "antigens", retrieveSchemaProps)
	}
	if _, ok := retrieveSchemaProps["include_siblings"]; ok {
		t.Errorf("retrieve input schema still has legacy %q property; want it gone (clean cut): %+v", "include_siblings", retrieveSchemaProps)
	}

	// JSON-level pin on the actual response shape: matched_tags, not
	// matched_antigens.
	root := seedPatient(t, e, "Rename Pin Patient")
	seedChildBead(t, e, root, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic"},
		map[string]any{"drug": "meropenem"})

	system := newServerT(t, e, SystemRole)
	_, out, err := system.retrieve(context.Background(), nil, retrieveIn{
		Tags: []string{"risk:nephrotoxic"},
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal retrieveOut: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal retrieveOut: %v", err)
	}
	items, _ := decoded["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("retrieve returned no items to inspect: %s", raw)
	}
	firstItem, _ := items[0].(map[string]any)
	if _, ok := firstItem["matched_antigens"]; ok {
		t.Errorf("retrieveOut item JSON still has legacy matched_antigens key: %s", raw)
	}
	if _, ok := firstItem["matched_tags"]; !ok {
		t.Errorf("retrieveOut item JSON missing matched_tags key: %s", raw)
	}
}

// schemaPropertiesT extracts inputSchema.properties as a map[string]any from
// the client-observed InputSchema shape (map[string]any per mcp.Tool's own
// doc comment: "From the client, this field will hold the default JSON
// marshaling of the server's input schema"), failing the test if the shape
// is not what AddTool's jsonschema-go inference is expected to produce.
func schemaPropertiesT(t *testing.T, inputSchema any) map[string]any {
	t.Helper()
	m, ok := inputSchema.(map[string]any)
	if !ok {
		t.Fatalf("InputSchema is %T, want map[string]any", inputSchema)
	}
	props, ok := m["properties"].(map[string]any)
	if !ok {
		t.Fatalf("InputSchema[\"properties\"] is %T, want map[string]any: %+v", m["properties"], m)
	}
	return props
}

func toolNamesT(tools *mcp.ListToolsResult) []string {
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	return names
}
